package cache

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testLayout(t testing.TB, variable bool) *Layout {
	t.Helper()
	identity := "UUID"
	l, e := Compile(Definition{PIL: 0, Identity: "payment/transactions", Version: 1 << 20, Fields: []Field{
		{Name: "event_id", Type: "UUID"}, {Name: "id", Type: identity}, {Name: "revision_id", Type: "U64"}, {Name: "duration", Type: "U64"}, {Name: "state", Type: "STRING", Values: []string{"pending", "authorized", "canceled"}}, {Name: "message", Type: "STRING"}, {Name: "amount", Type: "U64"},
	}, Resource: Binding{Format: Format, Service: "payment", Resource: "transactions", Major: 1, Timezone: "America/New_York", Roles: Roles{Event: "event_id", Object: "id", Revision: "revision_id", Duration: "duration"}, Path: []string{"state"}, Aggregations: []string{"count", "duration.count", "duration.sum", "duration.min", "duration.max"}}})
	if e != nil {
		t.Fatal(e)
	}
	return l
}
func testObject(n uint32) Value { return uuid(0, n) }

func uuid(ms int64, n uint32) Value {
	var id [16]byte
	binary.BigEndian.PutUint64(id[:8], uint64(ms)<<16)
	id[6] = 0x70
	id[8] = 0x80
	binary.BigEndian.PutUint32(id[12:], n)
	return Value{Direct, id[:]}
}
func testFrame(t testing.TB, l *Layout, at time.Time, n uint32, sparse bool) []byte {
	t.Helper()
	id := testObject(42)
	v := []Value{uuid(at.UnixMilli(), n), id, Uint(uint64(n), 8), Uint(uint64(n+10), 8), Uint(uint64(n%3), 1), Text("request completed"), Uint(999, 8)}
	if sparse {
		v[3] = Value{}
		v[4] = Value{}
		v[5] = Value{Null, nil}
		v[6] = Uint(uint64(n), 8)
	}
	raw, e := l.Encode(nil, v)
	if e != nil {
		t.Fatal(e)
	}
	return raw
}
func collect(t *testing.T, s *Store, b Branch, from, to time.Time) [][]byte {
	t.Helper()
	var rows [][]byte
	e := s.Search(context.Background(), Query{Branch: b, From: from, To: to}, func(_ uint64, b []byte) error { rows = append(rows, append([]byte(nil), b...)); return nil })
	if e != nil {
		t.Fatal(e)
	}
	return rows
}

func TestStoreLateSparseBranchesAndRecovery(t *testing.T) {
	for _, variable := range []bool{false, true} {
		t.Run(map[bool]string{false: "fixed", true: "variable"}[variable], func(t *testing.T) {
			l := testLayout(t, variable)
			start := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
			now := start.Add(72 * time.Hour)
			c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "authenticity", Layout: l, BatchRecords: 2, Now: func() time.Time { return now }}
			s, e := Open(c)
			if e != nil {
				t.Fatal(e)
			}
			first := testFrame(t, l, start, 1, false)
			late := testFrame(t, l, start.Add(-time.Hour), 2, true)
			next := testFrame(t, l, start.Add(24*time.Hour), 3, false)
			for _, b := range []Branch{Read, Write, Error, Debug, Trace} {
				for _, raw := range [][]byte{first, next, late} {
					if dup, e := s.Append(b, raw); e != nil || dup {
						t.Fatalf("append: %v %v", dup, e)
					}
				}
			}
			if e = s.Flush(); e != nil {
				t.Fatal(e)
			}
			for _, b := range []Branch{Read, Write, Error, Debug, Trace} {
				if dup, e := s.Append(b, first); e != nil || !dup {
					t.Fatalf("duplicate: %v %v", dup, e)
				}
			}
			if e = s.Close(); e != nil {
				t.Fatal(e)
			}
			s, e = Open(c)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			for _, b := range []Branch{Read, Write, Error, Debug, Trace} {
				rows := collect(t, s, b, start.Add(-2*time.Hour), start.Add(25*time.Hour))
				if len(rows) != 3 || !bytes.Equal(rows[0], late) || !bytes.Equal(rows[1], first) || !bytes.Equal(rows[2], next) {
					t.Fatalf("round trip %s", b.Name())
				}
				if dup, e := s.Append(b, late); e != nil || !dup {
					t.Fatalf("recovered duplicate: %v %v", dup, e)
				}
				path := filepath.Join(s.dir, b.Prefix()+".sum")
				_, e := os.Stat(path)
				if b.Aggregates() {
					if e != nil {
						t.Fatal(e)
					}
					groups, e := s.Summaries(b, start.Add(-2*time.Hour), now)
					if e != nil {
						t.Fatal(e)
					}
					var a Aggregate
					for _, g := range groups {
						for _, v := range g {
							if e = a.merge(v); e != nil {
								t.Fatal(e)
							}
						}
					}
					if a.Count != 3 || a.DurationCount != 2 || a.Sum != 24 {
						t.Fatalf("aggregate %+v", a)
					}
				} else {
					if !os.IsNotExist(e) {
						t.Fatalf("unexpected diagnostic aggregate: %v", e)
					}
					if _, e = s.Summaries(b, start, now); e == nil {
						t.Fatal("diagnostic summary accepted")
					}
				}
			}
			ranges, tail, e := s.PeriodRanges(Write, 2026, 244)
			if e != nil || tail != 6 || len(ranges) != 2 || ranges[0].End == nil || *ranges[0].End != 2 {
				t.Fatalf("late day ranges: %+v tail %d %v", ranges, tail, e)
			}
			count := 0
			e = s.Search(context.Background(), Query{Branch: Write, From: start.Add(-2 * time.Hour), To: now, Equals: map[string]Value{"state": Text("authorized")}}, func(_ uint64, _ []byte) error { count++; return nil })
			if e != nil || count != 1 {
				t.Fatalf("predicate %d %v", count, e)
			}
		})
	}
}

func TestRecoveryDiscardUncommittedTails(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "owner", Layout: l, Now: func() time.Time { return at.Add(2 * time.Hour) }}
	s, e := Open(c)
	if e != nil {
		t.Fatal(e)
	}
	frame := testFrame(t, l, at, 1, false)
	if _, e = s.Append(Write, frame); e != nil {
		t.Fatal(e)
	}
	dir := s.dir
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	for _, ext := range []string{"idx", "pods", "sum", "tri"} {
		f, e := os.OpenFile(filepath.Join(dir, "write."+ext), os.O_WRONLY|os.O_APPEND, 0)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.Write([]byte{1, 2, 3}); e != nil {
			t.Fatal(e)
		}
		f.Close()
	}
	s, e = Open(c)
	if e != nil {
		t.Fatal(e)
	}
	if rows := collect(t, s, Write, at.Add(-time.Second), at.Add(time.Second)); len(rows) != 1 || !bytes.Equal(rows[0], frame) {
		t.Fatal("recovery lost data")
	}
	s.Close()
	f, e := os.OpenFile(filepath.Join(dir, "write.pods"), os.O_RDWR, 0)
	if e != nil {
		t.Fatal(e)
	}
	f.WriteAt([]byte{0xff}, headerSize)
	f.Close()
	if bad, e := Open(c); e == nil {
		bad.Close()
		t.Fatal("committed corruption accepted")
	}
}
func TestOwnershipAndDefinitionConflict(t *testing.T) {
	l := testLayout(t, false)
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "owner", Layout: l}
	s, e := Open(c)
	if e != nil {
		t.Fatal(e)
	}
	if second, e := Open(c); e == nil {
		second.Close()
		t.Fatal("second writer accepted")
	}
	s.Close()
	c.Tenant = "another"
	if second, e := Open(c); e == nil {
		second.Close()
		t.Fatal("cross-tenant cache accepted")
	}
	c.Tenant = "owner"
	d := l.Definition
	d.Fields[6].Name = "other"
	c.Layout, e = Compile(d)
	if e != nil {
		t.Fatal(e)
	}
	if second, e := Open(c); e == nil {
		second.Close()
		t.Fatal("definition replacement accepted")
	}
}
func TestMaturityAndLateAggregateRejection(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	now := at.Add(10 * time.Minute)
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "owner", Layout: l, Now: func() time.Time { return now }}
	s, e := Open(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	s.Append(Write, testFrame(t, l, at, 1, false))
	g, e := s.Summaries(Write, at, at.Add(time.Hour))
	if e != nil || len(g) != 0 {
		t.Fatalf("premature %v %v", g, e)
	}
	now = at.Add(90 * time.Minute)
	g, e = s.Summaries(Write, at, now)
	if e != nil || len(g) != 1 {
		t.Fatalf("mature %v %v", g, e)
	}
	if _, e = s.Append(Write, testFrame(t, l, at.Add(time.Minute), 4, false)); e == nil {
		t.Fatal("late correction accepted")
	}
	g, e = s.Summaries(Write, at, now)
	if e != nil {
		t.Fatal(e)
	}
	var a Aggregate
	for _, v := range g[at.UnixMilli()] {
		a.merge(v)
	}
	if a.Count != 1 || a.Sum != 11 {
		t.Fatalf("late correction %+v", a)
	}
}

func TestFrameValidationAndReuse(t *testing.T) {
	l := testLayout(t, false)
	at := time.Now()
	raw := testFrame(t, l, at, 1, false)
	var f Frame
	if e := l.Scan(raw, &f); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < len(raw); i++ {
		if e := l.Scan(raw[:i], &f); e == nil {
			t.Fatalf("truncation %d accepted", i)
		}
	}
	if allocs := testing.AllocsPerRun(100, func() {
		if e := l.Scan(raw, &f); e != nil {
			panic(e)
		}
	}); allocs != 0 {
		t.Fatalf("scan allocations %f", allocs)
	}
	// The agreed PIL mask/order, little-endian scalars and cumulative offsets.
	d := l.Definition
	d.Fields = []Field{{Name: "event_id", Type: "UUID"}, {Name: "n", Type: "U16"}, {Name: "text", Type: "STRING"}, {Name: "other", Type: "STRING"}}
	d.Resource.Roles = Roles{Event: "event_id"}
	d.Resource.Path = nil
	d.Resource.Aggregations = nil
	l, e := Compile(d)
	if e != nil {
		t.Fatal(e)
	}
	id := uuid(at.UnixMilli(), 1)
	raw, e = l.Encode(nil, []Value{id, Uint(0x1234, 2), Text("hi"), Value{Null, nil}})
	if e != nil {
		t.Fatal(e)
	}
	want := append([]byte{0xad}, id.Bytes...)
	want = append(want, 0x34, 0x12, 2, 0, 'h', 'i')
	if !bytes.Equal(raw, want) {
		t.Fatalf("wire %x != %x", raw, want)
	}
}

func TestExportRustInterop(t *testing.T) {
	dir := os.Getenv("TRUNK_INTEROP_FIXTURE")
	if dir == "" {
		t.Skip("set TRUNK_INTEROP_FIXTURE to export a cross-language fixture")
	}
	l := testLayout(t, true)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "resource.map"), l.Bytes, 0600); e != nil {
		t.Fatal(e)
	}
	raw := append(testFrame(t, l, at, 1, false), testFrame(t, l, at, 2, true)...)
	if e := os.WriteFile(filepath.Join(dir, "frames.pil"), raw, 0600); e != nil {
		t.Fatal(e)
	}
}

func TestConcurrentWriterAndSnapshotReaders(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "owner", Layout: l, BatchRecords: 32, Now: func() time.Time { return at.Add(time.Minute) }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	frames := make([][]byte, 96)
	for i := range frames {
		frames[i] = testFrame(t, l, at.Add(time.Duration(i)*time.Millisecond), uint32(i), false)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, frame := range frames {
			if _, e := s.Append(Write, frame); e != nil {
				failures <- e
				return
			}
		}
	}()
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := 0; round < 6; round++ {
				var previous []byte
				e := s.SearchLive(context.Background(), Query{Branch: Write, From: at, To: at.Add(time.Hour)}, func(frame []byte) error {
					var f Frame
					if e := l.Scan(frame, &f); e != nil {
						return e
					}
					id := f.Values[l.Event].Bytes
					if previous != nil && bytes.Compare(previous, id) >= 0 {
						return errors.New("snapshot order or dedup violation")
					}
					previous = append(previous[:0], id...)
					return nil
				})
				if e != nil {
					failures <- e
					return
				}
			}
		}()
	}
	wg.Wait()
	close(failures)
	for e := range failures {
		t.Fatal(e)
	}
	s.config.Now = func() time.Time { return at.Add(2 * time.Hour) }
	if rows := collect(t, s, Write, at, at.Add(time.Hour)); len(rows) != len(frames) {
		t.Fatal("concurrent event loss")
	}
}

// commitLegacyFixture models bytes already produced by the old immediate-run
// writer. It is intentionally not an ingress path: read compatibility with
// interleaved files must survive enforcement of ordered new writes.
func commitLegacyFixture(t *testing.T, s *Store, branch Branch, raw []byte) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	b, e := s.openBranch(branch)
	if e != nil {
		t.Fatal(e)
	}
	var f Frame
	if e = s.layout.Scan(raw, &f); e != nil {
		t.Fatal(e)
	}
	id, e := eventID(f.Values[s.layout.Event])
	if e != nil {
		t.Fatal(e)
	}
	if e = s.accumulate(b, id, f.Values); e != nil {
		t.Fatal(e)
	}
	if e = s.commitBatch(b, []pending{{id, raw}}); e != nil {
		t.Fatal(e)
	}
}
