package cache

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/pelletier/go-toml/v2"
)

var configurationGroups = map[string][]string{
	"compression": {"pac.gzip_level", "summary.gzip_level"},
	"staging":     {"staging.max_bytes", "staging.max_records", "staging.max_disk_bytes"},
	"indexing":    {"search_index", "object_index", "cache_invalidation"},
	"replication": {"replication.index_period", "replication.population", "replication.provisional_tail"},
}

type configurationView struct {
	Scopes     [][]string               `json:"scopes"`
	Scope      []string                 `json:"scope"`
	File       string                   `json:"file"`
	Revision   string                   `json:"revision"`
	Exists     bool                     `json:"exists"`
	Inherit    bool                     `json:"inherit"`
	Local      map[string]any           `json:"local"`
	Effective  map[string]any           `json:"effective"`
	Transforms []configurationTransform `json:"transforms"`
}
type configurationTransform struct {
	Definition string `json:"definition"`
	Source     string `json:"source"`
}

func readConfiguration(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("configuration exceeds 1 MiB")
	}
	return raw, err
}
func flattenConfiguration(m map[string]any, prefix string, out map[string]any) {
	for k, v := range m {
		key := prefix + k
		if child, ok := v.(map[string]any); ok {
			flattenConfiguration(child, key+".", out)
		} else {
			out[key] = v
		}
	}
}
func policyValues(policy Policy) map[string]any {
	raw, _ := toml.Marshal(policy)
	var m map[string]any
	_ = toml.Unmarshal(raw, &m)
	result := map[string]any{}
	flattenConfiguration(m, "", result)
	return result
}
func (p *Endpoint) configurationHTTP(w http.ResponseWriter, r *http.Request, sources []*HTTP) {
	if r.Method != "GET" && r.Method != "PUT" {
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", 405)
		return
	}
	var scope []string
	if err := json.Unmarshal([]byte(r.URL.Query().Get("scope")), &scope); err != nil {
		http.Error(w, "scope must be a JSON path", 400)
		return
	}
	inventory, err := collectStorageInventory(sources, map[string]string{p.config.Cache: filepath.Join(p.config.Root, p.config.Cache)}, false)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if scope == nil {
		scope = []string{}
	}
	if inventory.nodes[storageKey(scope)] == nil {
		http.Error(w, "scope is not in the storage catalog", 400)
		return
	}
	// The Trunk config may be shared by caches in different roots.
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(p.config.Root), ".configurations.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer lock.Close()
	mode := syscall.LOCK_SH
	if r.Method == "PUT" {
		mode = syscall.LOCK_EX
	}
	if err = syscall.Flock(int(lock.Fd()), mode); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	path := configurationPath(p.config.Root, scope)
	raw, err := readConfiguration(path)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	revision := fmt.Sprintf("%x", sha256.Sum256(raw))
	document := map[string]any{"version": int64(1)}
	if len(raw) > 0 {
		if err = toml.Unmarshal(raw, &document); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	if r.Method == "PUT" {
		if r.Header.Get("If-Match") != `"`+revision+`"` {
			http.Error(w, "Configuration changed. Reload before saving.", 412)
			return
		}
		var update struct {
			Category string         `json:"category"`
			Values   map[string]any `json:"values"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.UseNumber()
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&update); err != nil {
			http.Error(w, "invalid configuration update", 400)
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			http.Error(w, "expected one document", 400)
			return
		}
		keys, ok := configurationGroups[update.Category]
		if !ok {
			http.Error(w, "unknown configuration category", 400)
			return
		}
		allowed := map[string]bool{}
		for _, key := range keys {
			allowed[key] = true
		}
		policy, ok := document["policy"].(map[string]any)
		if !ok {
			policy = map[string]any{}
			document["policy"] = policy
		}
		for key, value := range update.Values {
			if !allowed[key] || len(scope) == 4 && (strings.HasPrefix(key, "staging.") || key == "object_index") {
				http.Error(w, "setting is not allowed at this scope", 400)
				return
			}
			if n, ok := value.(json.Number); ok {
				v, e := n.Int64()
				if e != nil || v < 0 || v > 1<<53-1 {
					http.Error(w, "expected a nonnegative safe integer", 400)
					return
				}
				value = v
			}
			parts := strings.Split(key, ".")
			target := policy
			if len(parts) == 2 {
				child, ok := target[parts[0]].(map[string]any)
				if !ok {
					child = map[string]any{}
					target[parts[0]] = child
				}
				target = child
			}
			field := parts[len(parts)-1]
			if value == nil {
				delete(target, field)
			} else {
				target[field] = value
			}
		}
		// Empty tables must be absent: an empty staging table would reset inherited
		// fields to zero in the existing policy merge.
		for key, value := range policy {
			if child, ok := value.(map[string]any); ok && len(child) == 0 {
				delete(policy, key)
			}
		}
		next, e := toml.Marshal(document)
		if e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		if _, _, e = resolveScope(next, path, Policy{}, len(scope) == 4); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		if e = os.MkdirAll(filepath.Dir(path), 0700); e == nil {
			e = replace(path, next)
		}
		if e != nil {
			http.Error(w, "could not save configuration; reload to verify", 500)
			return
		}
		raw = next
		revision = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	var config Configuration
	if len(scope) > 0 {
		registry, e := readConfiguration(filepath.Join(p.config.Root, scope[0], "registry.cfg"))
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		if len(registry) > 0 {
			config, e = ParseConfiguration(registry)
			if e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
		}
	}
	effective, err := configuredScope(p.config.Root, scope, config)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	local := map[string]any{}
	if m, ok := document["policy"].(map[string]any); ok {
		flattenConfiguration(m, "", local)
	}
	view := configurationView{Scope: scope, File: filepath.Base(path), Revision: revision, Exists: len(raw) > 0, Inherit: true, Local: local, Effective: policyValues(effective), Scopes: [][]string{}, Transforms: []configurationTransform{}}
	if inherit, ok := document["inherit"].(bool); ok {
		view.Inherit = inherit
	}
	for _, node := range inventory.rows() {
		view.Scopes = append(view.Scopes, node.Path)
	}
	if len(scope) == 3 {
		for _, source := range sources {
			s := source.Store
			n := s.layout.Definition.Resource
			if scope[0] != s.config.Cache || scope[1] != n.Service || scope[2] != fmt.Sprintf("%s.v%d", n.Resource, n.Major) {
				continue
			}
			content, e := readConfiguration(filepath.Join(s.dir, "transforms.cfg"))
			if e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			view.Transforms = append(view.Transforms, configurationTransform{Definition: fmtDigestNative(s.layout.Digest), Source: string(bytes.TrimSpace(content))})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", `"`+revision+`"`)
	_ = json.NewEncoder(w).Encode(view)
}
