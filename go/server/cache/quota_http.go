package cache

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

// Quotas are persisted policy only. Deletion is deliberately not connected here.
// Intermediate limits gate descendant allocations; only leaves reserve space.
type quotaRecovery struct {
	Unit  string  `json:"unit"`
	Value float64 `json:"value"`
}
type quotaScope struct {
	Managed  *bool          `json:"managed,omitempty"`
	Path     []string       `json:"path"`
	Limit    *uint64        `json:"limitBytes,omitempty"`
	Recovery *quotaRecovery `json:"recovery,omitempty"`
}
type quotaPolicy struct {
	Managed *bool        `json:"managed,omitempty"`
	Pool    *uint64      `json:"poolBytes"`
	Scopes  []quotaScope `json:"scopes"`
}
type quotaDisk struct {
	Total     uint64 `json:"totalBytes"`
	Available uint64 `json:"availableBytes"`
	Owned     uint64 `json:"ownedBytes"`
	Maximum   uint64 `json:"maximumBytes"`
}
type quotaSettings struct {
	Policy   quotaPolicy `json:"policy"`
	Revision string      `json:"revision"`
	Disk     quotaDisk   `json:"disk"`
	Enforced bool        `json:"enforced"`
}

func scopeWithin(path, parent []string) bool {
	return len(path) >= len(parent) && slices.Equal(path[:len(parent)], parent)
}
func quotaManaged(flag *bool, depth int) bool {
	if flag != nil {
		return *flag
	}
	return depth == 0 || depth == 4
}
func validateQuotas(p quotaPolicy, maximum uint64) error {
	if len(p.Scopes) > 10000 {
		return fmt.Errorf("too many quota scopes")
	}
	rootManaged := quotaManaged(p.Managed, 0)
	if rootManaged && p.Pool != nil && *p.Pool > maximum {
		return fmt.Errorf("Trunk pool exceeds available disk capacity")
	}
	seen := map[string]bool{}
	for _, s := range p.Scopes {
		if len(s.Path) < 1 || len(s.Path) > 4 {
			return fmt.Errorf("invalid quota scope")
		}
		for _, part := range s.Path {
			if part == "" || len(part) > 256 {
				return fmt.Errorf("invalid scope name")
			}
		}
		raw, _ := json.Marshal(s.Path)
		key := string(raw)
		if seen[key] {
			return fmt.Errorf("duplicate quota scope")
		}
		seen[key] = true
		if quotaManaged(s.Managed, len(s.Path)) && s.Limit != nil && p.Pool == nil && rootManaged {
			return fmt.Errorf("set the Trunk pool before assigning limits")
		}
		if s.Recovery != nil {
			r := s.Recovery
			if len(s.Path) != 4 || math.IsNaN(r.Value) || math.IsInf(r.Value, 0) || r.Value < 0 || (r.Unit != "percent" && r.Unit != "bytes") || (r.Unit == "percent" && r.Value > 100) || (r.Unit == "bytes" && (r.Value > (1<<53)-1 || math.Trunc(r.Value) != r.Value)) {
				return fmt.Errorf("invalid leaf recovery setting")
			}
		}
	}
	pool := p.Pool
	if !rootManaged || pool == nil {
		pool = &maximum
	}
	gates := []quotaScope{{Path: []string{}, Limit: pool}}
	for _, scope := range p.Scopes {
		if quotaManaged(scope.Managed, len(scope.Path)) {
			gates = append(gates, scope)
		}
	}
	for _, gate := range gates {
		if gate.Limit == nil {
			continue
		}
		var assigned uint64
		for _, s := range p.Scopes {
			if !quotaManaged(s.Managed, len(s.Path)) || !scopeWithin(s.Path, gate.Path) {
				continue
			}
			if s.Limit != nil {
				if *s.Limit > *gate.Limit {
					return fmt.Errorf("scope limit exceeds its ancestor limit")
				}
				if len(s.Path) == 4 {
					if *s.Limit > *gate.Limit-assigned {
						return fmt.Errorf("leaf allocations exceed their shared pool")
					}
					assigned += *s.Limit
				}
			}
			if s.Recovery != nil && s.Recovery.Unit == "bytes" && s.Recovery.Value > float64(*gate.Limit) {
				return fmt.Errorf("recovery amount exceeds the scope limit")
			}
		}
	}
	return nil
}
func quotaCapacity(root string) (quotaDisk, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(root, &stat); err != nil {
		return quotaDisk{}, err
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return quotaDisk{}, err
	}
	device := rootInfo.Sys().(*syscall.Stat_t).Dev
	d := quotaDisk{Total: stat.Blocks * uint64(stat.Bsize), Available: stat.Bavail * uint64(stat.Bsize)}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		if st.Dev != device {
			return fmt.Errorf("quota root spans multiple filesystems; configure each storage facility separately")
		}
		if entry.Type().IsRegular() {
			d.Owned += uint64(st.Blocks) * 512
		}
		return nil
	})
	d.Maximum = min(d.Total, d.Available+d.Owned)
	return d, err
}
func readQuotaPolicy(path string) (quotaPolicy, string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return quotaPolicy{Scopes: []quotaScope{}}, "unset", nil
	}
	if err != nil {
		return quotaPolicy{}, "", err
	}
	var p quotaPolicy
	if err = json.Unmarshal(raw, &p); err != nil {
		return p, "", err
	}
	return p, fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}
func (p *Endpoint) quotaHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "PUT" {
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", 405)
		return
	}
	if r.URL.RawQuery != "" {
		http.Error(w, "no query parameters supported", 400)
		return
	}
	// A file lock serializes publications across endpoints sharing this facility.
	lock, err := os.OpenFile(filepath.Join(p.config.Root, ".quotas.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		http.Error(w, "could not open quota lock", 500)
		return
	}
	defer lock.Close()
	mode := syscall.LOCK_SH
	if r.Method == "PUT" {
		mode = syscall.LOCK_EX
	}
	if err = syscall.Flock(int(lock.Fd()), mode); err != nil {
		http.Error(w, "could not lock quotas", 500)
		return
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	path := filepath.Join(p.config.Root, "quotas.cfg")
	policy, revision, err := readQuotaPolicy(path)
	if err != nil {
		http.Error(w, "could not read quota settings", 500)
		return
	}
	disk, err := quotaCapacity(p.config.Root)
	if err != nil {
		http.Error(w, "could not measure disk capacity: "+err.Error(), 500)
		return
	}
	if r.Method == "PUT" {
		if r.Header.Get("If-Match") != `"`+revision+`"` {
			http.Error(w, "quota settings changed; reload before saving", 412)
			return
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
		decoder.DisallowUnknownFields()
		var next quotaPolicy
		if err = decoder.Decode(&next); err != nil {
			http.Error(w, "invalid quota settings", 400)
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			http.Error(w, "expected one settings document", 400)
			return
		}
		p.mu.Lock()
		sources := p.sources()
		p.mu.Unlock()
		inventory, inventoryErr := collectStorageInventory(sources, map[string]string{p.config.Cache: filepath.Join(p.config.Root, p.config.Cache)}, false)
		if inventoryErr != nil {
			http.Error(w, "could not verify storage inventory", 500)
			return
		}
		for _, scope := range next.Scopes {
			if inventory.nodes[storageKey(scope.Path)] == nil {
				http.Error(w, "quota scope is not in the physical storage catalog; reload storage", 400)
				return
			}
		}
		if err = validateQuotas(next, disk.Maximum); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if next.Scopes == nil {
			next.Scopes = []quotaScope{}
		}
		raw, _ := json.MarshalIndent(next, "", "  ")
		raw = append(raw, '\n')
		if err = replace(path, raw); err != nil {
			http.Error(w, "could not publish quota settings; reload to verify", 500)
			return
		}
		policy = next
		revision = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	w.Header().Set("ETag", `"`+revision+`"`)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(quotaSettings{policy, revision, disk, false})
}
