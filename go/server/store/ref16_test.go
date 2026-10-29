package store

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"trie-trunk/go/codec"
)

func TestReferenceWidthsRoundTripAndRejectOverflow(t *testing.T) {
	for _, format := range []uint32{3, 4} {
		for _, compact := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/compact=%v", format, compact), func(t *testing.T) {
				p := partition{compact: compact, schema: &schema{definition: codec.Definition{Storage: &codec.StoragePolicy{Format: format}}}}
				var rows []row
				start := uint64(16)
				for i, leaf := range []uint32{0, 1, 65535} {
					id, err := codec.NewUUIDv7(time.UnixMilli(1000 + int64(i)))
					if err != nil {
						t.Fatal(err)
					}
					rows = append(rows, row{Id: id, Leaf: leaf, Start: start, End: start + uint64(i+1)})
					start += uint64(i + 1)
				}
				b, err := p.encodeRun(rows)
				if err != nil {
					t.Fatal(err)
				}
				width, header := p.rowLayout()
				wanted := 36
				if compact {
					wanted = 28
				}
				if format == 4 {
					wanted -= 2
				}
				if width != wanted || len(b) != header+len(rows)*wanted {
					t.Fatal(width, len(b))
				}
				got, err := p.decodeRun(b)
				if err != nil {
					t.Fatal(err)
				}
				for i, r := range got {
					if r.Id != rows[i].Id || r.Leaf != rows[i].Leaf || r.Start != rows[i].Start || r.End != rows[i].End {
						t.Fatal(i, r, rows[i])
					}
				}
				for n := 0; n < len(b); n++ {
					if _, err := p.decodeRun(b[:n]); err == nil {
						t.Fatalf("accepted truncation %d", n)
					}
				}
				rows[0].Leaf = 65536
				_, err = p.encodeRun(rows)
				if (err != nil) != (format == 4) {
					t.Fatal("incorrect reference overflow handling", format, err)
				}
			})
		}
	}
}

func TestReference16MaturityLateArrivalDedupAndRecovery(t *testing.T) {
	c, rev, definition, now := modernFixture(t, "America/New_York")
	d, err := codec.ParseDefinition(definition, rev)
	if err != nil {
		t.Fatal(err)
	}
	d.Storage.Format = 4
	rev, definition, err = d.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	*now = time.Date(2026, 9, 23, 12, 10, 0, 0, time.UTC)
	m := mustOpen(t, c, rev, definition)
	_, first := modernPayload(t, now.Add(-20*time.Minute), 1)
	_, second := modernPayload(t, now.Add(-21*time.Minute), 1)
	for _, b := range [][]byte{first, second} {
		if _, err = m.Append(rev, b); err != nil {
			t.Fatal(err)
		}
	}
	if err = m.Flush(); err != nil {
		t.Fatal(err)
	}
	s := m.schemas[rev.Digest]
	scopes, err := s.scopes(c.Root, m.config.Tap)
	if err != nil || len(scopes) != 1 {
		t.Fatal(scopes, err)
	}
	dir := filepath.Join(s.directory(c.Root, m.config.Tap), scopes[0].Dir)
	before, _ := os.ReadFile(filepath.Join(dir, "events.pods"))
	index, _ := os.ReadFile(filepath.Join(dir, "events.idx"))
	if string(index[:4]) != "TIDA" || order.Uint32(index[4:8]) != 4 || len(index) != 36+8+4+2*34 {
		t.Fatal("wrong active layout", len(index))
	}
	*now = now.Add(5 * time.Minute)
	if err = m.Flush(); err != nil {
		t.Fatal(err)
	}
	index, _ = os.ReadFile(filepath.Join(dir, "events.idx"))
	if string(index[:4]) != "TIDC" || len(index) != 36+8+12+2*26 {
		t.Fatal("wrong compact layout", len(index))
	}
	after, _ := os.ReadFile(filepath.Join(dir, "events.pods"))
	if !bytes.Equal(before, after) {
		t.Fatal("maturity rewrote payload")
	}
	_, late := modernPayload(t, now.Add(-23*time.Minute), 1)
	if dup, err := m.Append(rev, late); err != nil || dup {
		t.Fatal(dup, err)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	m, err = Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if dup, err := m.Append(rev, first); err != nil || !dup {
		t.Fatal("dedup", dup, err)
	}
	n := 0
	if err = m.Scan(rev, *now, func(codec.Record) error { n++; return nil }); err != nil || n != 3 {
		t.Fatal(n, err)
	}
}

func TestReference16TrieCapacityRetainsValuesInPayload(t *testing.T) {
	// Even an explicitly larger legacy Config cannot exceed the new disk width.
	s := &schema{definition: codec.Definition{Storage: &codec.StoragePolicy{Format: 4}}}
	tr := &trie{schema: s, nodes: make([]node, 65536), edges: make(map[edge]uint32)}
	dynamic, err := codec.MarshalCompact(codec.Record{9: "kept"}, codec.PIL)
	if err != nil {
		t.Fatal(err)
	}
	r, err := tr.prepare(record{Path: []token{{Key: 3, Value: mustPILInteger(t, 7)}}, Dynamic: dynamic}, Config{MaxNodes: 100000})
	if err != nil {
		t.Fatal(err)
	}
	values, err := codec.Decode(r.Dynamic, codec.PIL)
	if err != nil || r.Leaf != 0 || len(tr.nodes) != 65536 || values[3] != uint64(7) || values[9] != "kept" {
		t.Fatal(r.Leaf, len(tr.nodes), values, err)
	}
}

func mustPILInteger(t *testing.T, n uint64) []byte {
	b, e := diskEncoder.Marshal(n)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
