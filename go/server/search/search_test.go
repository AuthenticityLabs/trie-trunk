package search

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"trie-trunk/go/codec"
	"trie-trunk/go/server/store"
)

func TestSearchRendersIndexThenLineageThenPopulation(t *testing.T) {
	c, rev, data, now := fixture(t)
	m := mustOpen(t, c, rev, data)
	ids := []string{
		appendSearchRecord(t, m, rev, *now, codec.Record{2: uint64(1), 21: map[string]any{"name": "Synthetic Customer"}}),
		appendSearchRecord(t, m, rev, now.Add(time.Millisecond), codec.Record{2: uint64(1), 21: map[string]any{"name": "Synthetic Customer"}}),
	}
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	for _, pretty := range []bool{false, true} {
		for _, test := range []struct {
			headers []string
			order   []string
		}{
			{nil, []string{"id", "time", "level", "http_status", "category", "duration", "fields"}},
			{[]string{"time_start", "time_begin", "fields", "level", "id", "fields"}, []string{"fields", "level", "id", "time", "http_status", "category", "duration"}},
		} {
			var output bytes.Buffer
			q := Config{Root: c.Root, Start: *now, End: now.Add(time.Second), Headers: test.headers, Pretty: pretty}
			if err := Run(context.Background(), q, &output); err != nil {
				t.Fatal(err)
			}
			decoder := json.NewDecoder(&output)
			for _, id := range ids {
				if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
					t.Fatal(token, err)
				}
				for _, name := range test.order {
					if token, err := decoder.Token(); err != nil || token != name {
						t.Fatalf("field order: got %v, want %s: %v", token, name, err)
					}
					var value any
					if err := decoder.Decode(&value); err != nil {
						t.Fatal(err)
					}
					if name == "id" && value != id {
						t.Fatal(value, id)
					}
				}
				if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
					t.Fatal(token, err)
				}
			}
			if _, err := decoder.Token(); err != io.EOF {
				t.Fatal(err)
			}
		}
	}
}

func searchResults(t *testing.T, c Config) []map[string]any {
	t.Helper()
	var out []map[string]any
	if err := readSearch(context.Background(), c, func(r map[string]any) error { out = append(out, r); return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

func fileDigests(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	if err := filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err == nil {
			out[path] = sha256.Sum256(b)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func appendSearchRecord(t *testing.T, m *store.Manager, rev codec.Revision, at time.Time, extra codec.Record) string {
	t.Helper()
	id, err := codec.NewUUIDv7(at)
	if err != nil {
		t.Fatal(err)
	}
	r := codec.Record{8: id, 12: uint64(2234), 3: uint64(5), 14: uint64(401)}
	for k, v := range extra {
		r[k] = v
	}
	b, err := codec.MarshalCompact(r, codec.CBOR)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Append(rev, b); err != nil {
		t.Fatal(err)
	}
	return id.String()
}

func TestSearchWindowMergesTapsVersionsAndRuns(t *testing.T) {
	c, _, _, now := fixture(t)
	start := now.Add(-11 * time.Minute)
	end := start.Add(3 * time.Minute)
	var want []string
	for _, tap := range []string{"east", "west"} {
		c.Tap = tap
		m, err := store.Open(c)
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		for _, version := range []uint32{41, 42} {
			d := testDefinition()
			d.Version = version
			level := "ERROR"
			if version == 42 {
				level = "FAILURE"
			}
			d.Fields[3] = codec.Field{Name: "level", Enums: map[uint16]string{5: level}}
			rev, data, err := d.Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if err = m.Install(rev, data); err != nil {
				t.Fatal(err)
			}

			want = append(want, appendSearchRecord(t, m, rev, start.Add(2*time.Minute), nil))
			appendSearchRecord(t, m, rev, end, nil)
			if err = m.Flush(); err != nil {
				t.Fatal(err)
			}
			want = append(want, appendSearchRecord(t, m, rev, start.Add(time.Minute), nil))
			want = append(want, appendSearchRecord(t, m, rev, start, nil))
			if err = m.Flush(); err != nil {
				t.Fatal(err)
			}
			appendSearchRecord(t, m, rev, start.Add(-time.Millisecond), nil)
			if err = m.Flush(); err != nil {
				t.Fatal(err)
			}
		}
	}
	before := fileDigests(t, c.Root)
	q := Config{Root: c.Root, Start: start, End: end}
	records := searchResults(t, q)
	sort.Strings(want)
	var got []string
	levels := map[string]int{}
	for _, r := range records {
		got = append(got, r["id"].(string))
		levels[r["level"].(string)]++
		if r["duration"] != uint64(2234) || r["http_status"] != uint64(401) {
			t.Fatal(r)
		}
	}
	if !reflect.DeepEqual(got, want) || levels["ERROR"] != 6 || levels["FAILURE"] != 6 {
		t.Fatal(got, want, levels)
	}
	q.Tap = "east"
	if n := len(searchResults(t, q)); n != 6 {
		t.Fatal(n)
	}
	q.Start = end
	q.End = end.Add(time.Minute)
	if n := len(searchResults(t, q)); n != 2 {
		t.Fatal(n)
	}
	if !reflect.DeepEqual(before, fileDigests(t, c.Root)) {
		t.Fatal("search modified store files")
	}
}

func TestSearchCrossDayExpansionAndLiveSnapshot(t *testing.T) {
	c, _, _, now := fixture(t)
	d := testDefinition()
	d.Domains = map[string]map[uint16]string{"tier": {1: "PREMIUM"}}
	d.Fields[4] = codec.Field{Name: "tier", Domain: "tier"}
	d.Fields[22] = codec.Field{Name: "items", Array: codec.Schema{1: {Name: "tier", Domain: "tier"}}}
	rev, data, err := d.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	m := mustOpen(t, c, rev, data)
	day := now.UTC().Truncate(24 * time.Hour)
	first := appendSearchRecord(t, m, rev, day.Add(-time.Millisecond), codec.Record{4: uint64(1), 16: codec.Record{0: uint64(1), 3: uint64(401)}, 21: map[string]any{"name": "Synthetic Customer"}, 22: []codec.Record{{1: uint64(1)}}})
	second := appendSearchRecord(t, m, rev, day.Add(time.Minute), codec.Record{12: uint64(0)})
	if err = m.Flush(); err != nil {
		t.Fatal(err)
	}
	q := Config{Root: c.Root, Start: day.Add(-time.Hour), End: day.Add(time.Hour)}
	out := searchResults(t, q)
	if len(out) != 2 || out[0]["id"] != first || out[1]["id"] != second {
		t.Fatal(out)
	}
	if out[0]["time"] != "2026-09-22T23:59:59.999Z" || out[0]["duration"] != uint64(2234) {
		t.Fatal("incorrect start time or duration across midnight", out[0])
	}
	if out[1]["time"] != "2026-09-23T00:01:00Z" || out[1]["duration"] != uint64(0) {
		t.Fatal("incorrect start time or zero duration", out[1])
	}
	for _, row := range out {
		for _, name := range []string{"time_begin", "time_start", "time_end"} {
			if _, exists := row[name]; exists {
				t.Fatal("obsolete derived timestamp", name, row)
			}
		}
	}
	if out[0]["tier"] != "PREMIUM" || out[0]["error"].(map[string]any)["code"] != "UNEXPECTED" || out[0]["fields"].(map[string]any)["name"] != "Synthetic Customer" || out[0]["items"].([]any)[0].(map[string]any)["tier"] != "PREMIUM" {
		t.Fatal(out[0])
	}

	q.Start = day
	visited := 0
	if err = readSearch(context.Background(), q, func(r map[string]any) error {
		visited++
		appendSearchRecord(t, m, rev, day.Add(2*time.Minute), nil)
		return m.Flush()
	}); err != nil {
		t.Fatal(err)
	}
	if visited != 1 || len(searchResults(t, q)) != 2 {
		t.Fatal("live append escaped the captured index length")
	}
}

func TestSearchPartialTailAndCorruption(t *testing.T) {
	for _, damage := range []string{"partial_index", "partial_trie", "orphan_pop", "bad_index", "bad_pop", "bad_trie", "missing_pop"} {
		t.Run(damage, func(t *testing.T) {
			c, rev, data, now := fixture(t)
			m := mustOpen(t, c, rev, data)
			appendSearchRecord(t, m, rev, *now, nil)
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			version := filepath.Join(c.Root, "0", fmt.Sprint(rev.Version))
			day := filepath.Join(version, now.Format("2006/002"))
			var path string
			switch damage {
			case "partial_index", "bad_index":
				path = filepath.Join(day, "log.idx")
			case "partial_trie", "bad_trie":
				path = filepath.Join(version, "log.tri")
			default:
				path = filepath.Join(day, "log.pop")
			}
			if damage == "missing_pop" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				f, err := os.OpenFile(path, os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(damage, "bad_") {
					_, err = f.WriteAt([]byte{0xff}, 16)
				} else {
					_, err = f.Seek(0, 2)
					if err == nil {
						_, err = f.Write([]byte{0, 0, 0, 20, 0, 0, 0, 0, 1})
					}
				}
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			before := fileDigests(t, c.Root)
			count := 0
			err := readSearch(context.Background(), Config{Root: c.Root, Start: now.Add(-time.Minute), End: now.Add(time.Minute)}, func(map[string]any) error { count++; return nil })
			damaged := strings.HasPrefix(damage, "bad_") || damage == "missing_pop"
			if damaged && err == nil || !damaged && (err != nil || count != 1) {
				t.Fatal(count, err)
			}
			var pil bytes.Buffer
			pilErr := Run(context.Background(), Config{Root: c.Root, Start: now.Add(-time.Minute), End: now.Add(time.Minute), PIL: true}, &pil)
			if damaged && pilErr == nil || !damaged && pilErr != nil {
				t.Fatal("direct PIL corruption handling differs", pilErr)
			}
			if !reflect.DeepEqual(before, fileDigests(t, c.Root)) {
				t.Fatal("reader changed damaged files")
			}
		})
	}
}

func TestSearchCancellationAndAbsentDay(t *testing.T) {
	c, rev, data, now := fixture(t)
	m := mustOpen(t, c, rev, data)
	appendSearchRecord(t, m, rev, *now, nil)
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	q := Config{Root: c.Root, Start: now.Add(-time.Minute), End: now.Add(time.Minute)}
	stopped := errors.New("stop output")
	if err := readSearch(context.Background(), q, func(map[string]any) error { return stopped }); !errors.Is(err, stopped) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := readSearch(ctx, q, func(map[string]any) error { t.Fatal("visited after cancellation"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	before := fileDigests(t, c.Root)
	q.Start = now.Add(-24 * time.Hour)
	q.End = q.Start.Add(time.Minute)
	if len(searchResults(t, q)) != 0 || !reflect.DeepEqual(before, fileDigests(t, c.Root)) {
		t.Fatal("absent-day search created files")
	}
	q.Tap = "../escape"
	if err := readSearch(context.Background(), q, func(map[string]any) error { return nil }); err == nil {
		t.Fatal("accepted invalid tap")
	}
}

func fixture(t *testing.T) (store.Config, codec.Revision, []byte, *time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 23, 12, 16, 0, 0, time.UTC)
	c := store.Config{Root: t.TempDir(), Now: func() time.Time { return now }, BatchRecords: 2}
	d := testDefinition()
	d.Version = 3<<20 | 42
	rev, data, e := d.Prepare()
	if e != nil {
		t.Fatal(e)
	}
	return c, rev, data, &now
}

func mustOpen(t *testing.T, c store.Config, rev codec.Revision, d []byte) *store.Manager {
	t.Helper()
	m, e := store.Open(c)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { m.Close() })
	if e = m.Install(rev, d); e != nil {
		t.Fatal(e)
	}
	return m
}

func testDefinition() codec.Definition {
	return codec.Definition{Version: 4, Timezone: "UTC", Fields: codec.Schema{
		8: {Name: "id", Encoding: "uuidv7"}, 12: {Name: "duration", Unit: "us"},
		2:  {Name: "category", Enums: map[uint16]string{1: "SUMMARY"}},
		3:  {Name: "level", Enums: map[uint16]string{3: "INFO", 5: "ERROR"}},
		14: {Name: "http_status"}, 19: {Name: "uri"}, 21: {Name: "fields"},
		16: {Name: "error", Object: codec.Schema{0: {Name: "code", Enums: map[uint16]string{1: "UNEXPECTED"}}, 3: {Name: "status"}}},
	}, Path: []codec.Key{3, 14, 2}, Aggregations: []string{"count", "duration.count", "duration.sum", "duration.min", "duration.max"}}
}

func TestSearchFractionalBoundariesAndLiteralValues(t *testing.T) {
	c, rev, data, now := fixture(t)
	m := mustOpen(t, c, rev, data)
	at := now.Truncate(time.Millisecond)
	first := appendSearchRecord(t, m, rev, at, codec.Record{21: map[string]any{
		"unsigned": ^uint64(0), "signed": int64(-9007199254740993), "bytes": []byte{0, 255},
		"nested": []any{true, nil, map[string]any{"text": "雪 <value>\nnext", "float": 1.25}},
	}})
	second := appendSearchRecord(t, m, rev, at.Add(time.Millisecond), nil)
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	out := searchResults(t, Config{Root: c.Root, Start: at, End: at.Add(time.Nanosecond)})
	if len(out) != 1 || out[0]["id"] != first {
		t.Fatal(out)
	}
	fields := out[0]["fields"].(map[string]any)
	if fields["unsigned"] != ^uint64(0) || fields["signed"] != json.Number("-9007199254740993") || fields["bytes"] != "AP8=" {
		t.Fatal(fields)
	}
	nested := fields["nested"].([]any)
	if nested[0] != true || nested[1] != nil || nested[2].(map[string]any)["text"] != "雪 <value>\nnext" {
		t.Fatal(nested)
	}
	out = searchResults(t, Config{Root: c.Root, Start: at.Add(time.Nanosecond), End: at.Add(2 * time.Millisecond)})
	if len(out) != 1 || out[0]["id"] != second {
		t.Fatal(out)
	}
	if len(searchResults(t, Config{Root: c.Root, Start: at.Add(time.Nanosecond), End: at.Add(2 * time.Nanosecond)})) != 0 {
		t.Fatal("fractional empty window matched an event")
	}
}

func TestSearchCancelsDuringOutput(t *testing.T) {
	c, rev, data, now := fixture(t)
	c.BatchRecords = 512
	m := mustOpen(t, c, rev, data)
	for i := 0; i < 2000; i++ {
		appendSearchRecord(t, m, rev, now.Add(time.Duration(i)*time.Millisecond), nil)
	}
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &cancelOutput{cancel: cancel}
	err := Run(ctx, Config{Root: c.Root, Start: *now, End: now.Add(time.Minute)}, output)
	if !errors.Is(err, context.Canceled) || output.calls == 0 {
		t.Fatal(output.calls, err)
	}
}

type cancelOutput struct {
	cancel context.CancelFunc
	calls  int
}

func (w *cancelOutput) Write(data []byte) (int, error) { w.calls++; w.cancel(); return len(data), nil }
