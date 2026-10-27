package cache

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"time"
)

// Inventory follows the registered definitions and opened physical branches.
// A resource's minors are implementation details, aggregated into one branch leaf.
// No event field, shared REF16 value, or quota setting can create a storage node.
type storageNode struct {
	Path         []string `json:"path"`
	Kind         string   `json:"kind"`
	Allocated    uint64   `json:"allocated"`
	Logical      uint64   `json:"logical"`
	OwnAllocated uint64   `json:"ownAllocated"`
	OwnLogical   uint64   `json:"ownLogical"`
	Files        uint64   `json:"files"`
}
type storageOwner struct {
	resource []string
	branches map[string][]string
}
type storageInventory struct {
	nodes   map[string]*storageNode
	roots   map[string]string
	owners  map[string]storageOwner
	samples []storageSample
}

func storageKey(path []string) string { raw, _ := json.Marshal(path); return string(raw) }
func (v *storageInventory) add(path []string) {
	kinds := []string{"trunk", "cache", "service", "resource", "branch"}
	for depth := 0; depth <= len(path); depth++ {
		prefix := append([]string{}, path[:depth]...)
		key := storageKey(prefix)
		if v.nodes[key] == nil {
			v.nodes[key] = &storageNode{Path: prefix, Kind: kinds[depth]}
		}
	}
}
func collectStorageInventory(sources []*HTTP, roots map[string]string, history bool) (*storageInventory, error) {
	v := &storageInventory{nodes: map[string]*storageNode{}, roots: map[string]string{}, owners: map[string]storageOwner{}, samples: []storageSample{}}
	v.add([]string{})
	for cache, root := range roots {
		v.roots[cache] = root
		v.add([]string{cache})
	}
	for _, source := range sources {
		s := source.Store
		if s == nil {
			continue
		}
		if _, seen := v.owners[s.dir]; seen {
			continue
		}
		if err := s.inventoryStorage(v, history); err != nil {
			return nil, err
		}
	}
	return v, nil
}
func (s *Store) inventoryStorage(v *storageInventory, history bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return err
	}
	if digest, ok := s.catalog.Definitions[s.binding]; !ok || digest != s.layout.Digest {
		return fmt.Errorf("storage definition is not cataloged")
	}
	r := s.layout.Definition.Resource
	scope := []string{s.config.Cache, r.Service, fmt.Sprintf("%s.v%d", r.Resource, r.Major)}
	v.roots[s.config.Cache] = s.root
	v.add(scope)
	owner := storageOwner{resource: scope, branches: map[string][]string{}}
	for branch, b := range s.branches {
		if b.journal == nil || b.idx == nil || b.pop == nil {
			return fmt.Errorf("incomplete physical branch inventory")
		}
		path := append(append([]string{}, scope...), branch.Name())
		owner.branches[branch.Name()] = path
		v.add(path)
		if !history {
			continue
		}
		hours := map[int64]*storageSample{}
		add := func(hour int64, bytes float64, count uint64) {
			row := hours[hour]
			if row == nil {
				row = &storageSample{Path: path, Hour: hour}
				hours[hour] = row
			}
			row.Bytes += bytes
			row.Events += count
		}
		for _, run := range b.runs {
			add(utcHour(run.MinMS), float64(run.PopEnd-run.PopStart+(run.End-run.Start+1)*IndexRowSize), run.End-run.Start)
		}
		// Pending descriptors expose UUID and length without decoding or copying PIL.
		live := b.live.snapshot(nil, 0, time.UnixMilli(0), time.UnixMilli(1<<48))
		for _, record := range live.records {
			var id [16]byte
			for i := range id {
				id[i] = byte(record.id[i])
			}
			add(utcHour(eventTime(id[:])), float64(record.length), 1)
		}
		live.close()
		for _, row := range hours {
			v.samples = append(v.samples, *row)
		}
	}
	v.owners[filepath.Clean(s.dir)] = owner
	return nil
}
func (v *storageInventory) rows() []storageNode {
	rows := make([]storageNode, 0, len(v.nodes))
	for _, node := range v.nodes {
		rows = append(rows, *node)
	}
	sort.Slice(rows, func(i, j int) bool { return storageKey(rows[i].Path) < storageKey(rows[j].Path) })
	return rows
}
