package cache

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"trie-trunk/go/internal/resourceapi"
)

// ResourcePath recognizes the public cache-scoped data API. Storage filenames
// remain catalog identities, not public routes.
func ResourcePath(path string) bool { return resourceapi.IsPath(path) }

// translateResource selects an existing handler without changing its batching,
// byte framing, or storage semantics. The translated paths are internal only.
func translateResource(r *http.Request, cache string, sources []*HTTP) (*http.Request, error) {
	if !ResourcePath(r.URL.Path) || r.URL.EscapedPath() != r.URL.Path {
		return nil, fmt.Errorf("invalid resource endpoint")
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if parts[0] != cache {
		return nil, fmt.Errorf("unknown cache")
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("invalid parameters")
	}
	for key, values := range q {
		if len(values) != 1 {
			return nil, fmt.Errorf("duplicate parameter: %s", key)
		}
		if key == "tap" || key == "from" || key == "to" || key == "begin" || key == "end" || key == "cache" || key == "collection" {
			return nil, fmt.Errorf("use branch, beginTime, and endTime")
		}
	}
	branch := q.Get("branch")
	if branch != "" {
		p := strings.Split(branch, "/")
		if len(p) != 3 {
			return nil, fmt.Errorf("branch must be service/resource/branch")
		}
		for _, part := range p {
			if part != "*" && !name(part) {
				return nil, fmt.Errorf("invalid branch")
			}
		}
	}
	q.Del("branch")
	tap := cache + "/" + branch
	if branch == "" {
		tap = cache + "/*/*/*"
	}
	for _, pair := range [][2]string{{"beginTime", "from"}, {"endTime", "to"}} {
		if !q.Has(pair[0]) {
			continue
		}
		n, e := strconv.ParseInt(q.Get(pair[0]), 10, 64)
		if e != nil || n < 0 || n > 1<<48-1 {
			return nil, fmt.Errorf("%s must be epoch milliseconds", pair[0])
		}
		q.Del(pair[0])
		q.Set(pair[1], time.UnixMilli(n).UTC().Format(time.RFC3339Nano))
	}
	copy := r.Clone(r.Context())
	u := *r.URL
	copy.URL = &u
	u.RawPath = ""
	switch parts[1] {
	case "seeds":
		u.Path = "/collect"
		q.Set("tap", tap)
		if !q.Has("to") {
			q.Set("to", "now")
		}
	case "indexes":
		if q.Get("mode") == "search" {
			u.Path = "/events"
			q.Del("mode")
		} else {
			if q.Has("mode") {
				return nil, fmt.Errorf("invalid index mode")
			}
			u.Path = "/collect"
			q.Set("data", "none")
		}
		q.Set("tap", tap)
		if !q.Has("to") {
			q.Set("to", "now")
		}
	case "registries":
		u.Path = "/definitions"
		if q.Get("kind") == "bindings" {
			if r.Method != http.MethodGet {
				return nil, fmt.Errorf("registry bindings require GET")
			}
			u.Path = "/schemas"
			q.Del("kind")
			body, _ := json.Marshal(map[string]any{"tap": tap, "known": strings.FieldsFunc(r.Header.Get("Definition-Known"), func(c rune) bool { return c == ',' })})
			copy.Method = http.MethodPost
			copy.Body = io.NopCloser(strings.NewReader(string(body)))
			copy.ContentLength = int64(len(body))
		} else if q.Has("kind") {
			return nil, fmt.Errorf("invalid registry kind")
		}
	case "collections":
		if branch == "" || strings.Contains(branch, "*") || !strings.HasSuffix(branch, "/write") {
			return nil, fmt.Errorf("collections require an exact write branch")
		}
		q.Set("collection", strings.TrimSuffix(branch, "/write"))
		q.Set("cache", cache)
		if q.Get("kind") == "state" {
			u.Path = "/objects/state"
		} else if q.Get("kind") == "" {
			u.Path = "/objects/history"
			if !q.Has("from") {
				q.Set("from", time.UnixMilli(0).UTC().Format(time.RFC3339Nano))
			}
			if !q.Has("to") {
				now := time.Now()
				if len(sources) > 0 && sources[0].Store.config.Now != nil {
					now = sources[0].Store.config.Now()
				}
				q.Set("to", now.UTC().Format(time.RFC3339Nano))
			}
		} else {
			return nil, fmt.Errorf("invalid collection kind")
		}
		q.Del("kind")
	case "summaries", "timelines":
		if parts[1] == "summaries" && r.Method == http.MethodPost {
			if branch != "" {
				return nil, fmt.Errorf("summary pack references supply their own branches")
			}
			u.Path = "/summaries"
			break
		}
		var selected *HTTP
		var b Branch
		for _, api := range sources {
			if q.Get("definition") != "" && q.Get("definition") != fmtDigestNative(api.Store.layout.Digest) {
				continue
			}
			branches, e := api.branch(tap)
			if e != nil || len(branches) != 1 {
				continue
			}
			if selected != nil {
				return nil, fmt.Errorf("select a definition for this branch")
			}
			selected, b = api, branches[0]
		}
		if selected == nil {
			return nil, fmt.Errorf("unknown branch")
		}
		ext := "tri"
		if parts[1] == "summaries" {
			ext = "sum"
		}
		u.Path = "/" + cache + "/" + selected.Store.binding + "/" + b.Name() + "." + ext
		q.Del("definition")
		for _, pair := range [][2]string{{"from", "begin"}, {"to", "end"}} {
			t, e := time.Parse(time.RFC3339Nano, q.Get(pair[0]))
			if pair[0] == "to" && !q.Has("to") {
				t, e = selected.Store.config.Now(), nil
				if ext == "sum" {
					t = t.Truncate(24 * time.Hour).Add(24 * time.Hour)
				} else {
					t = t.Truncate(time.Hour).Add(time.Hour)
				}
			}
			if e != nil {
				return nil, fmt.Errorf("invalid time bounds")
			}
			unit := time.Hour
			if ext == "sum" {
				unit = 24 * time.Hour
			}
			if !t.Equal(t.Truncate(unit)) {
				return nil, fmt.Errorf("range must align to %s", unit)
			}
			v := fmt.Sprintf("%02d%03d", t.Year()-2000, t.YearDay())
			if ext != "sum" {
				v += fmt.Sprintf("%02d", t.Hour())
			}
			q.Del(pair[0])
			q.Set(pair[1], v)
		}
	}
	u.RawQuery = q.Encode()
	return copy, nil
}

func controlPath(path string) bool {
	return trunkRequest(&http.Request{URL: &url.URL{Path: path}}) || path == "/ingest" || path == "/rejections" || path == "/caches" || path == "/configurations" || path == "/storage" || path == "/storage/quotas"
}

func (p *Endpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if ResourcePath(r.URL.Path) {
		p.mu.Lock()
		sources := p.sources()
		p.mu.Unlock()
		translated, err := translateResource(r, p.config.Cache, sources)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		p.serveHTTP(w, translated)
		return
	}
	if !controlPath(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	p.serveHTTP(w, r)
}

func (h *HTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if ResourcePath(r.URL.Path) {
		sources := h.sources
		if len(sources) == 0 && h.Store != nil {
			sources = []*HTTP{h}
		}
		if len(sources) == 0 {
			http.NotFound(w, r)
			return
		}
		translated, err := translateResource(r, sources[0].Store.config.Cache, sources)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.serveHTTP(w, translated)
		return
	}
	if !controlPath(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	h.serveHTTP(w, r)
}
