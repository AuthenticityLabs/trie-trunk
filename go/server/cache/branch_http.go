package cache

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Branch-file URLs identify catalog resources, never paths opened from user input.
type branchFile struct {
	path, directory, extension string
	branch                     Branch
}

func branchFilePath(path string) (branchFile, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) < 2 {
		return branchFile{}, false
	}
	for _, p := range parts[:len(parts)-1] {
		if !name(p) {
			return branchFile{}, false
		}
	}
	file := strings.Split(parts[len(parts)-1], ".")
	if len(file) != 2 {
		return branchFile{}, false
	}
	var branch Branch
	found := false
	for b := Read; b <= Trace; b++ {
		if file[0] == b.Name() {
			branch = b
			found = true
			break
		}
	}
	if !found {
		return branchFile{}, false
	}
	switch file[1] {
	case "tri", "sum", "idx", "tvy", "pods":
	default:
		return branchFile{}, false
	}
	return branchFile{strings.TrimPrefix(path, "/"), strings.Join(parts[:len(parts)-1], "/"), file[1], branch}, true
}

// Compact UTC bounds use years 2000..2099 and half-open intervals.
func compactTime(raw string, hourly bool) (time.Time, error) {
	width := 5
	if hourly {
		width = 7
	}
	if len(raw) != width {
		return time.Time{}, fmt.Errorf("expected UTC %s", map[bool]string{true: "YYDDDHH", false: "YYDDD"}[hourly])
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return time.Time{}, fmt.Errorf("invalid UTC bound")
		}
	}
	year, _ := strconv.Atoi(raw[:2])
	day, _ := strconv.Atoi(raw[2:5])
	hour := 0
	if hourly {
		hour, _ = strconv.Atoi(raw[5:])
	}
	stamp := time.Date(2000+year, 1, day, hour, 0, 0, 0, time.UTC)
	if day < 1 || stamp.Year() != 2000+year || hour > 23 {
		return time.Time{}, fmt.Errorf("invalid UTC day or hour")
	}
	return stamp, nil
}
func branchWindow(r *http.Request, hourly bool, allowed ...string) (time.Time, time.Time, error) {
	q := r.URL.Query()
	keys := map[string]bool{"begin": true, "end": true}
	for _, k := range allowed {
		keys[k] = true
	}
	for k, v := range q {
		if !keys[k] || len(v) != 1 {
			return time.Time{}, time.Time{}, fmt.Errorf("unsupported or duplicate branch range parameter")
		}
	}
	from, err := compactTime(q.Get("begin"), hourly)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	to, err := compactTime(q.Get("end"), hourly)
	if err != nil || !from.Before(to) {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid half-open UTC range")
	}
	return from, to, nil
}
func (h *HTTP) branchFileHTTP(w http.ResponseWriter, r *http.Request, file branchFile) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET required", 405)
		return
	}
	if r.URL.EscapedPath() != r.URL.Path {
		http.Error(w, "invalid catalog resource path", 400)
		return
	}
	if file.extension != "tri" && file.extension != "idx" && file.extension != "pods" && file.extension != "sum" {
		http.Error(w, "branch artifact replication is not implemented yet", 501)
		return
	}
	allowed := []string{}
	if file.extension == "idx" || file.extension == "pods" {
		allowed = append(allowed, "states")
	}
	from, to, err := branchWindow(r, file.extension != "sum", allowed...)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	apis := h.sources
	if len(apis) == 0 && h.Store != nil {
		apis = []*HTTP{h}
	}
	var selected *HTTP
	for _, api := range apis {
		if api.Store.config.Cache+"/"+api.Store.binding != file.directory {
			continue
		}
		if selected != nil {
			http.Error(w, "select a definition for this resource", 409)
			return
		}
		selected = api
	}
	if selected == nil {
		http.NotFound(w, r)
		return
	}
	if file.extension == "sum" {
		selected.branchSummaries(w, r, file, from, to)
		return
	}
	if file.extension == "idx" || file.extension == "pods" {
		// State selection is carried by the client plan. Compressed hour packs
		// are copied whole; no per-event server traversal is performed here.
		query := url.Values{"format": {"native"}, "tap": {selected.tap(file.branch)}, "definition": {fmtDigestNative(selected.Store.layout.Digest)}, "from": {from.Format(time.RFC3339)}, "to": {to.Format(time.RFC3339)}}
		if file.extension == "idx" {
			query.Set("data", "none")
		}
		copy := r.Clone(r.Context())
		u := *r.URL
		copy.URL = &u
		copy.URL.Path = "/collect"
		copy.URL.RawPath = ""
		copy.URL.RawQuery = query.Encode()
		w.Header().Set("Trunk-Path", file.path)
		selected.nativeCollect(w, copy)
		return
	}
	s := selected.Store
	s.mu.Lock()
	b, err := s.openBranch(file.branch)
	if err == nil {
		err = s.flush(b)
	}
	if err != nil {
		s.mu.Unlock()
		http.Error(w, err.Error(), 500)
		return
	}
	first, sealed := b.time.first(), b.time.sealed()
	a, z := max(from.UnixMilli(), first), min(to.UnixMilli(), sealed)
	var raw []byte
	if a < z {
		raw, err = b.time.window(a, z, true)
	}
	definition, generation, sequence := fmtDigestNative(s.layout.Digest), fmt.Sprintf("%x", b.generation), s.generationSequence(file.branch)
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Trunk-Format", "time-trie/0")
	w.Header().Set("Trunk-Source", selected.tap(file.branch))
	w.Header().Set("Trunk-Path", file.path)
	w.Header().Set("Definition-Version", definition)
	w.Header().Set("Trunk-Generation", generation)
	w.Header().Set("Trunk-Generation-Sequence", strconv.FormatUint(sequence, 10))
	w.Header().Set("Trunk-Retained-From", time.UnixMilli(first).UTC().Format(time.RFC3339Nano))
	w.Header().Set("Trunk-Sealed-Through", time.UnixMilli(sealed).UTC().Format(time.RFC3339Nano))
	if len(raw) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	_, _ = w.Write(raw)
}
