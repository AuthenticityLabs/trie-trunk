package store

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"trie-trunk/go/codec"
)

const statisticsWindow = 30 * 24 * time.Hour
const hourMillis int64 = 3_600_000

type trieStatistics struct {
	ranges    map[int64][2]int64
	hours     map[int64]map[uint32]uint64
	through   map[string]uint64 // Population end, stable across index compaction/reopen.
	dirty     bool
	published time.Time
}

func newTrieStatistics() *trieStatistics {
	return &trieStatistics{hours: map[int64]map[uint32]uint64{}, ranges: map[int64][2]int64{}, through: map[string]uint64{}, dirty: true}
}
func (s *schema) observe(dir string, rows []row, now time.Time) {
	if s.statistics == nil {
		return
	}
	stats := s.statistics
	through := stats.through[dir]
	cutoff := now.Add(-statisticsWindow).UnixMilli() / hourMillis * hourMillis
	for _, r := range rows {
		if r.End <= through {
			continue
		}
		hour := millis(r.Id) / hourMillis * hourMillis
		if hour >= cutoff {
			if stats.hours[hour] == nil {
				stats.hours[hour] = map[uint32]uint64{}
			}
			stats.hours[hour][r.Leaf]++
			at := millis(r.Id)
			bounds, ok := stats.ranges[hour]
			if !ok {
				bounds = [2]int64{at, at}
			} else {
				bounds[0] = min(bounds[0], at)
				bounds[1] = max(bounds[1], at)
			}
			stats.ranges[hour] = bounds
			stats.dirty = true
		}
		if r.End > stats.through[dir] {
			stats.through[dir] = r.End
		}
	}
}

type TrieStatistics struct {
	Format      int                   `json:"format"`
	Object      string                `json:"object_name"`
	Major       uint32                `json:"major_version"`
	Minor       uint32                `json:"minor_version"`
	Digest      string                `json:"definition_digest"`
	From        time.Time             `json:"from_inclusive"`
	Through     time.Time             `json:"through_exclusive"`
	Events      uint64                `json:"events"`
	WithoutTrie uint64                `json:"without_trie_path"`
	StoredNodes int                   `json:"stored_nodes"`
	ActiveNodes int                   `json:"active_nodes"`
	MeanDepth   float64               `json:"mean_path_length"`
	Fields      []TrieFieldStatistics `json:"fields"`
	Nodes       []TrieNodeStatistics  `json:"nodes"`
	Hours       []TrieHourStatistics  `json:"hours"`
}
type TrieHourStatistics struct {
	From    time.Time         `json:"from_inclusive"`
	Through time.Time         `json:"through_exclusive"`
	Leaves  map[uint32]uint64 `json:"leaf_frequencies"`
}
type TrieNodeStatistics struct {
	ID           uint32    `json:"node"`
	Parent       uint32    `json:"parent"`
	Field        codec.Key `json:"field"`
	Value        uint64    `json:"value_reference"`
	Visits       uint64    `json:"visits"`
	Terminations uint64    `json:"terminations"`
}
type TrieFieldStatistics struct {
	Field              codec.Key         `json:"field"`
	Name               string            `json:"name"`
	Present            uint64            `json:"present"`
	Cardinality        int               `json:"cardinality"`
	Nodes              int               `json:"active_nodes"`
	RepeatedValues     int               `json:"repeated_value_nodes"`
	Entropy            float64           `json:"entropy_bits"`
	ConditionalEntropy float64           `json:"entropy_given_prefix_bits"`
	Frequencies        map[uint64]uint64 `json:"value_frequencies"`
}

func entropy(counts map[uint64]uint64, total uint64) float64 {
	var h float64
	for _, count := range counts {
		if count > 0 {
			p := float64(count) / float64(total)
			h -= p * math.Log2(p)
		}
	}
	return h
}

// Only the partial boundary hours are reread. Complete hours use accumulated
// leaf frequencies; neither path reconstructs events or reads population values.
func (m *Manager) partialHour(s *schema, start, end time.Time) (map[uint32]uint64, error) {
	counts := map[uint32]uint64{}
	for _, sc := range m.known[s.revision.Digest] {
		if !sc.Start.Before(end) || !sc.End.After(start) {
			continue
		}
		err := func() error {
			f, err := os.Open(filepath.Join(s.directory(m.config.Root, m.config.Tap), sc.Dir, s.filename("idx")))
			if err != nil {
				return err
			}
			defer f.Close()
			info, err := f.Stat()
			if err != nil {
				return err
			}
			var h [8]byte
			if _, err = f.ReadAt(h[:], 0); err != nil {
				return err
			}
			if order.Uint32(h[4:]) != s.definition.Storage.Format || string(h[:4]) != "TIDA" && string(h[:4]) != "TIDC" {
				return fmt.Errorf("invalid statistics index header")
			}
			p := partition{schema: s, compact: string(h[:4]) == "TIDC"}
			for offset := int64(fileHeaderSize + indexMetadataSize); offset < info.Size(); {
				if _, err = f.ReadAt(h[:], offset); err != nil {
					return err
				}
				n := int64(order.Uint32(h[:4]))
				if n == 0 || n > maxBlockSize || n > info.Size()-offset-8 {
					return fmt.Errorf("invalid statistics index block")
				}
				data := make([]byte, int(n))
				if _, err = f.ReadAt(data, offset+8); err != nil {
					return err
				}
				if checksum(data) != order.Uint32(h[4:]) {
					return fmt.Errorf("statistics index checksum mismatch")
				}
				rows, err := p.decodeRun(data)
				if err != nil {
					return err
				}
				for _, r := range rows {
					at := millis(r.Id)
					if at >= start.UnixMilli() && at < end.UnixMilli() {
						counts[r.Leaf]++
					}
				}
				offset += 8 + n
			}
			return nil
		}()
		if err != nil {
			return nil, err
		}
	}
	return counts, nil
}

func (m *Manager) publishStatistics(s *schema, now time.Time, force bool) error {
	stats := s.statistics
	if stats == nil || m.config.recoverOnly {
		return nil
	}
	now = now.UTC().Truncate(time.Millisecond)
	if !force && !stats.dirty && stats.published.Truncate(time.Minute).Equal(now.Truncate(time.Minute)) {
		return nil
	}
	from := now.Add(-statisticsWindow)
	first := from.UnixMilli() / hourMillis * hourMillis
	for hour := range stats.hours {
		if hour < first {
			delete(stats.hours, hour)
			delete(stats.ranges, hour)
		}
	}
	var hours []int64
	for hour := range stats.hours {
		if hour < now.UnixMilli() {
			hours = append(hours, hour)
		}
	}
	sort.Slice(hours, func(i, j int) bool { return hours[i] < hours[j] })
	result := TrieStatistics{Format: 1, Object: s.definition.Object, Major: s.revision.Version >> 20, Minor: s.revision.Version & minorMask, Digest: hex.EncodeToString(s.revision.Digest[:]), From: from, Through: now, Fields: []TrieFieldStatistics{}, Nodes: []TrieNodeStatistics{}, Hours: []TrieHourStatistics{}}
	leaves := map[uint32]uint64{}
	for _, hour := range hours {
		start, end := time.UnixMilli(hour).UTC(), time.UnixMilli(hour+hourMillis).UTC()
		counts := stats.hours[hour]
		if start.Before(from) || end.After(now) {
			if start.Before(from) {
				start = from
			}
			if end.After(now) {
				end = now
			}
			var err error
			bounds := stats.ranges[hour]
			if bounds[0] < from.UnixMilli() || bounds[1] >= now.UnixMilli() {
				counts, err = m.partialHour(s, start, end)
			}
			if err != nil {
				return err
			}
		}
		if len(counts) == 0 {
			continue
		}
		result.Hours = append(result.Hours, TrieHourStatistics{start, end, counts})
		for leaf, count := range counts {
			leaves[leaf] += count
			result.Events += count
		}
	}
	tr, err := acquireTrie(m.config, s)
	if err != nil {
		return err
	}
	defer tr.release()
	result.StoredNodes = len(tr.nodes) - 1
	visits := make([]uint64, len(tr.nodes))
	for leaf, count := range leaves {
		if uint64(leaf) >= uint64(len(visits)) {
			return fmt.Errorf("statistics references unknown trie node")
		}
		visits[leaf] = count
	}
	for i := len(visits) - 1; i > 0; i-- {
		visits[tr.nodes[i].Parent] += visits[i]
	}
	result.WithoutTrie = leaves[0]
	fields := map[codec.Key]*TrieFieldStatistics{}
	parents := map[codec.Key]map[uint32]map[uint64]uint64{}
	for _, key := range s.path {
		fields[key] = &TrieFieldStatistics{Field: key, Name: s.definition.Fields[key].Name, Frequencies: map[uint64]uint64{}}
		parents[key] = map[uint32]map[uint64]uint64{}
	}
	var traversals uint64
	for i := 1; i < len(tr.nodes); i++ {
		count := visits[i]
		if count == 0 {
			continue
		}
		n := tr.nodes[i]
		var value uint64
		if err := decoder.Unmarshal(n.Value, &value); err != nil {
			return err
		}
		result.Nodes = append(result.Nodes, TrieNodeStatistics{uint32(i), n.Parent, n.Key, value, count, leaves[uint32(i)]})
		f := fields[n.Key]
		if f == nil {
			return fmt.Errorf("statistics node outside definition path")
		}
		f.Present += count
		f.Nodes++
		f.Frequencies[value] += count
		if parents[n.Key][n.Parent] == nil {
			parents[n.Key][n.Parent] = map[uint64]uint64{}
		}
		parents[n.Key][n.Parent][value] += count
		traversals += count
	}
	result.ActiveNodes = len(result.Nodes)
	if result.Events > 0 {
		result.MeanDepth = float64(traversals) / float64(result.Events)
	}
	for _, key := range s.path {
		f := fields[key]
		f.Cardinality = len(f.Frequencies)
		f.RepeatedValues = f.Nodes - f.Cardinality
		f.Entropy = entropy(f.Frequencies, f.Present)
		for _, counts := range parents[key] {
			var total uint64
			for _, count := range counts {
				total += count
			}
			f.ConditionalEntropy += float64(total) / float64(f.Present) * entropy(counts, total)
		}
		result.Fields = append(result.Fields, *f)
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err = replaceSnapshot(filepath.Join(s.directory(m.config.Root, m.config.Tap), "statistics"), append(data, '\n')); err != nil {
		return err
	}
	stats.dirty = false
	stats.published = now
	return nil
}
func (m *Manager) publishAllStatistics(now time.Time) error {
	for _, s := range m.schemas {
		if err := m.publishStatistics(s, now, false); err != nil {
			return err
		}
	}
	return nil
}
