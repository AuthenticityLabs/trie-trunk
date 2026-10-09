package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"trie-trunk/go/codec"
)

func TestSharedTrieAndPopulationLayout(t *testing.T) {
	c, rev, definition, now := fixture(t)
	c.Tap = "sandbox"
	m := mustOpen(t, c, rev, definition)
	for _, at := range []time.Time{*now, now.Add(-24 * time.Hour)} {
		_, data := payload(t, at, 2234, 5)
		if _, err := m.Append(rev, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	var shared *trie
	for _, p := range m.partitions {
		if shared != nil && shared != p.trie {
			t.Fatal("days do not share one trie")
		}
		shared = p.trie
		if len(p.runs) != 1 {
			t.Fatal("missing run")
		}
		data := make([]byte, 4+indexRowSize)
		if _, err := p.idx.ReadAt(data, fileHeaderSize+8); err != nil {
			t.Fatal(err)
		}
		rows, err := decodeRun(data)
		if err != nil {
			t.Fatal(err)
		}
		pop, err := p.readPopRun(rows)
		if err != nil {
			t.Fatal(err)
		}
		fields, err := codec.Decode(pop, codec.CBOR)
		if err != nil {
			t.Fatal(err)
		}
		if fields[12] != uint64(2234) || fields[19] != "/secrets" {
			t.Fatal(fields)
		}
		for _, key := range []codec.Key{8, 3, 14, 2} {
			if _, ok := fields[key]; ok {
				t.Fatalf("shared or uuid field %d repeated in pop", key)
			}
		}
		dir := filepath.Dir(p.idx.Name())
		if filepath.Base(dir) != "266" && filepath.Base(dir) != "265" {
			t.Fatal(dir)
		}
		if _, err := os.Stat(filepath.Join(dir, "log.tri")); !os.IsNotExist(err) {
			t.Fatal("daily trie exists")
		}
	}
	if len(shared.nodes) != 4 {
		t.Fatalf("expected one three-edge path across days, got %d", len(shared.nodes)-1)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(versionDir(c.Root, c.Tap, rev.Version), "log.tri")
	header, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{'T', 'T', 'R', 'I', 0, 0, 0, 2}
	if !bytes.Equal(header[:fileHeaderSize], want) {
		t.Fatalf("unexpected header %x", header[:fileHeaderSize])
	}
	m = mustOpen(t, c, rev, definition)
	for _, at := range []time.Time{*now, now.Add(-24 * time.Hour)} {
		count := 0
		if err := m.Scan(rev, at, func(r codec.Record) error {
			count++
			if r[12] != uint64(2234) || r[3] != uint64(5) {
				t.Fatal(r)
			}
			return nil
		}); err != nil || count != 1 {
			t.Fatal(count, err)
		}
	}
}

func TestPopulationRecovery(t *testing.T) {
	for _, damage := range []string{"orphan", "partial_index", "truncated_referenced", "corrupt_referenced"} {
		t.Run(damage, func(t *testing.T) {
			c, rev, definition, now := fixture(t)
			m := mustOpen(t, c, rev, definition)
			_, data := payload(t, *now, 5, 5)
			if _, err := m.Append(rev, data); err != nil {
				t.Fatal(err)
			}
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(versionDir(c.Root, "0", rev.Version), dayPath(*now))
			popPath := filepath.Join(dir, "log.pop")
			stat, err := os.Stat(popPath)
			if err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(popPath, os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			switch damage {
			case "orphan", "partial_index":
				if _, err = appendBlock(f, []byte{0xa0}); err != nil {
					t.Fatal(err)
				}
				if damage == "partial_index" {
					idx, err := os.OpenFile(filepath.Join(dir, "log.idx"), os.O_APPEND|os.O_WRONLY, 0600)
					if err != nil {
						t.Fatal(err)
					}
					_, err = idx.Write([]byte{0, 0, 0, 50, 0, 0, 0, 0, 1})
					if err != nil {
						t.Fatal(err)
					}
					idx.Close()
				}
			case "truncated_referenced":
				err = f.Truncate(stat.Size() - 1)
			case "corrupt_referenced":
				_, err = f.WriteAt([]byte{0xff}, fileHeaderSize+8)
			}
			if err != nil {
				t.Fatal(err)
			}
			f.Close()
			opened, err := Open(c)
			if damage == "orphan" || damage == "partial_index" {
				if err != nil {
					t.Fatal(err)
				}
				opened.Close()
				after, _ := os.Stat(popPath)
				if after.Size() != stat.Size() {
					t.Fatal("unreferenced population bytes retained")
				}
			} else if err == nil {
				opened.Close()
				t.Fatal("accepted damaged committed population")
			}
		})
	}
}

func TestIndexRangesRejectOverlapAndOverflow(t *testing.T) {
	id, _ := codec.NewUUIDv7(time.Now())
	valid := row{Id: id, Start: 16, End: 18}
	for _, r := range []row{{Id: id, Start: 18, End: 16}, {Id: id, Start: 16, End: 16}, {Id: id, Start: 16, End: ^uint64(0)}} {
		if _, err := decodeRun(encodeRun([]row{r})); err == nil {
			t.Fatal("accepted invalid range")
		}
	}
	if _, err := decodeRun(append(encodeRun([]row{valid}), 0)); err == nil {
		t.Fatal("accepted trailing index data")
	}
}

func TestTapIsolation(t *testing.T) {
	c, rev, d, now := fixture(t)
	c.Tap = "east"
	a := mustOpen(t, c, rev, d)
	c.Tap = "west"
	b := mustOpen(t, c, rev, d)
	_, data := payload(t, *now, 1, 5)
	for _, m := range []*Manager{a, b} {
		if dup, err := m.Append(rev, data); dup || err != nil {
			t.Fatal(dup, err)
		}
	}
	c.Tap = "../escape"
	if _, err := Open(c); err == nil {
		t.Fatal("accepted escaping tap")
	}
}

func TestDayAndRollupPaths(t *testing.T) {
	c := Config{Root: "logs", Tap: "sandbox"}
	at := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	p, _, _, err := rollupPath(c, 42, Weekly, at)
	if err != nil || filepath.ToSlash(p) != "logs/sandbox/42/2026/log.w53" {
		t.Fatal(p, err)
	}
	if _, err = parseDay("2025/366"); err == nil {
		t.Fatal("invalid non-leap day")
	}
	if _, err = parseDay("2024/366"); err != nil {
		t.Fatal(err)
	}
}

func TestImmutableRollups(t *testing.T) {
	c, rev, d, now := fixture(t)
	m := mustOpen(t, c, rev, d)
	at := now.AddDate(-1, 0, 0)
	groups := map[uint32]Aggregate{0: {Count: 2, Sum: 8, Min: 3, Max: 5}}
	for _, resolution := range []Resolution{Daily, Weekly, Monthly, Yearly} {
		if err := m.PublishRollup(rev, resolution, at, groups); err != nil {
			t.Fatal(err)
		}
		if err := m.PublishRollup(rev, resolution, at, groups); err != nil {
			t.Fatal(err)
		}
		got, err := m.ReadRollup(rev, resolution, at)
		if err != nil || !reflect.DeepEqual(got, groups) {
			t.Fatal(got, err)
		}
		changed := map[uint32]Aggregate{0: {Count: 1, Sum: 4, Min: 4, Max: 4}}
		if err := m.PublishRollup(rev, resolution, at, changed); !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if err := m.PublishRollup(rev, Daily, *now, groups); err == nil {
		t.Fatal("published open day")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	m = mustOpen(t, c, rev, d)
	got, err := m.ReadRollup(rev, Daily, at)
	if err != nil || !reflect.DeepEqual(got, groups) {
		t.Fatal(got, err)
	}
}

func FuzzStorageBlocks(f *testing.F) {
	id, _ := codec.NewUUIDv7(time.Now())
	f.Add(encodeRun([]row{{Id: id, Start: 16, End: 18}}))
	f.Add([]byte{0, 0, 0, 1})
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > maxBlockSize {
			return
		}
		_, _ = decodeRun(b)
		_, _ = decodeSummary(b)
		_, _, _ = decodeRollup(b)
	})
}
