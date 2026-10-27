package cache

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type storageSample struct {
	Path   []string `json:"path"`
	Hour   int64    `json:"hour"`
	Bytes  float64  `json:"bytes"`
	Events uint64   `json:"events"`
}
type storageSnapshot struct {
	Nodes        []storageNode   `json:"nodes"`
	Samples      []storageSample `json:"samples"`
	Allocated    uint64          `json:"allocated"`
	Logical      uint64          `json:"logical"`
	Shared       uint64          `json:"shared"`
	Unattributed uint64          `json:"unattributed"`
	Captured     int64           `json:"captured"`
}

func (h *HTTP) storageHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET required", 405)
		return
	}
	if r.URL.RawQuery != "" {
		http.Error(w, "no query parameters supported", 400)
		return
	}
	sources := h.sources
	if len(sources) == 0 && h.Store != nil {
		sources = []*HTTP{h}
	}
	inventory, err := collectStorageInventory(sources, h.storageRoots, true)
	if err != nil {
		http.Error(w, "could not load storage catalog: "+err.Error(), 500)
		return
	}
	result := storageSnapshot{Samples: inventory.samples, Captured: time.Now().UnixMilli()}
	// Measure real files once. Branch files stay with their registered physical
	// branch; collection/resource/registry files remain at their owning parent.
	// Unknown files contribute to parent overhead, never invent new leaf nodes.
	for cache, root := range inventory.roots {
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if err = r.Context().Err(); err != nil {
				return err
			}
			if !d.Type().IsRegular() {
				return nil
			}
			info, err := d.Info()
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			logical := uint64(info.Size())
			allocated := logical
			if st, ok := info.Sys().(*syscall.Stat_t); ok {
				allocated = uint64(st.Blocks) * 512
			}
			owner := []string{cache}
			rel, _ := filepath.Rel(root, path)
			parts := strings.Split(rel, string(filepath.Separator))
			if len(parts) >= 3 {
				resource := []string{cache, parts[0], parts[1]}
				if inventory.nodes[storageKey(resource)] != nil {
					owner = resource
				}
			}
			if len(parts) >= 4 {
				dir := filepath.Join(root, parts[0], parts[1], parts[2])
				if entry, ok := inventory.owners[dir]; ok {
					owner = entry.resource
					branchName, _, _ := strings.Cut(parts[3], ".")
					if branch, exists := entry.branches[branchName]; exists {
						owner = branch
					}
				}
			}
			for depth := 0; depth <= len(owner); depth++ {
				node := inventory.nodes[storageKey(owner[:depth])]
				node.Allocated += allocated
				node.Logical += logical
				node.Files++
			}
			node := inventory.nodes[storageKey(owner)]
			node.OwnAllocated += allocated
			node.OwnLogical += logical
			if node.Kind != "branch" {
				result.Shared += allocated
			}
			result.Allocated += allocated
			result.Logical += logical
			return nil
		})
		if err != nil {
			http.Error(w, "could not measure storage: "+err.Error(), 500)
			return
		}
	}
	// Hour distributions are estimates weighted by recorded PAC/index bytes and
	// pending frame lengths. Leaf totals are exact filesystem allocation; shared
	// metadata is not assigned fictitious event times or reclaimable leaf space.
	weights := map[string]float64{}
	for _, sample := range result.Samples {
		weights[storageKey(sample.Path)] += sample.Bytes
	}
	var attributed uint64
	for key, weight := range weights {
		if weight > 0 {
			attributed += inventory.nodes[key].Allocated
		}
	}
	for i := range result.Samples {
		sample := &result.Samples[i]
		key := storageKey(sample.Path)
		if weights[key] > 0 {
			sample.Bytes *= float64(inventory.nodes[key].Allocated) / weights[key]
		}
	}
	result.Unattributed = result.Allocated - attributed
	result.Nodes = inventory.rows()
	sort.Slice(result.Samples, func(i, j int) bool {
		if result.Samples[i].Hour == result.Samples[j].Hour {
			return storageKey(result.Samples[i].Path) < storageKey(result.Samples[j].Path)
		}
		return result.Samples[i].Hour < result.Samples[j].Hour
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
