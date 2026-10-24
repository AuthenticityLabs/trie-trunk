package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestCoverageAndPackedGaps(t *testing.T) {
	got, e := Missing([]Span{{10, 30}, {40, 60}}, []Span{{0, 12}, {15, 25}, {45, 55}})
	want := []Span{{12, 15}, {25, 30}, {40, 45}, {55, 60}}
	if e != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%v %v", got, e)
	}
	extents := []Extent{{Span{100, 120}, 0}, {Span{140, 160}, 20}, {Span{120, 140}, 40}}
	gotExt, e := Resolve(extents, Span{110, 150})
	if e != nil || !reflect.DeepEqual(gotExt, []Extent{{Span{110, 120}, 10}, {Span{120, 140}, 40}, {Span{140, 150}, 20}}) {
		t.Fatalf("%v %v", gotExt, e)
	}
	if _, e = Resolve(extents[:2], Span{110, 150}); e == nil {
		t.Fatal("unfilled gap accepted")
	}
}

func TestCompleteHourCollectionAndGapResend(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "owner", Layout: l, Now: func() time.Time { return at.AddDate(0, 0, 3) }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	for _, n := range []uint32{0, 2} {
		if _, e = s.Append(Write, testFrame(t, l, at.Add(time.Duration(n)*time.Hour), n, false)); e != nil {
			t.Fatal(e)
		}
	}
	var m Manifest
	chunks := map[string]int{}
	request := CollectionRequest{Branch: Write, Day: at, PopulationFrom: at, PopulationTo: at.Add(time.Hour)}
	collect := func() error {
		return s.Collect(context.Background(), request, func(v Manifest) error { m = v; return nil }, func(c NativeChunk) error {
			if sha256.Sum256(c.Data) != c.Digest {
				t.Fatal("chunk digest")
			}
			chunks[c.Kind]++
			return nil
		})
	}
	if e = collect(); e != nil {
		t.Fatal(e)
	}
	if m.Tail != 4 || len(m.Index) != 2 || m.Index[0] != (Span{1, 2}) || m.Index[1] != (Span{3, 4}) || len(m.Population) != 2 || chunks["map"] != 1 || chunks["trie"] != 1 || chunks["index"] != 2 || chunks["pac"] != 2 || m.Provisional {
		t.Fatalf("%+v %+v", m, chunks)
	}
	request.Have = Coverage{Definition: m.Definition, Generation: m.Generation, Index: m.Index, Population: m.Population, TrieNodes: m.TrieNodes}
	clear(chunks)
	if e = collect(); e != nil {
		t.Fatal(e)
	}
	if chunks["map"]+chunks["trie"]+chunks["index"]+chunks["pac"] != 0 {
		t.Fatalf("duplicate transfer %v", chunks)
	}
	// A missing hour resends its whole block without touching covered hours.
	request.Have.Index = []Span{{1, 2}}
	clear(chunks)
	if e = collect(); e != nil {
		t.Fatal(e)
	}
	if m.Tail != 4 || chunks["index"] != 1 || chunks["pac"] != 1 {
		t.Fatalf("gap %+v %+v", m, chunks)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if e = s.Collect(canceled, request, func(Manifest) error { return nil }, func(NativeChunk) error { return nil }); !errors.Is(e, context.Canceled) {
		t.Fatalf("cancellation %v", e)
	}
	request.Day = at.AddDate(0, 0, -1)
	request.PopulationFrom = time.Time{}
	request.PopulationTo = time.Time{}
	if e = collect(); e != nil || len(m.Index) != 0 || m.Checkpoint == 0 {
		t.Fatalf("known empty %+v %v", m, e)
	}
}

func TestObjectHistoryAndIndependentInvalidation(t *testing.T) {
	l := testLayout(t, true)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "owner", Layout: l, ObjectIndex: true, Now: func() time.Time { return at.Add(2 * time.Hour) }}
	s, e := Open(c)
	if e != nil {
		t.Fatal(e)
	}
	id := testObject(42)
	before, e := s.Token(id)
	if e != nil {
		t.Fatal(e)
	}
	for b := Read; b <= Trace; b++ {
		if _, e = s.Append(b, testFrame(t, l, at, uint32(b+1), false)); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	loc, e := s.ObjectLocations(id)
	if e != nil || len(loc) != 1 || loc[0].Row != 1 {
		t.Fatalf("history %+v %v", loc, e)
	}
	if current, e := s.StillCurrent(id, before); e != nil || current {
		t.Fatalf("error failed to invalidate %v %v", current, e)
	}
	after, e := s.Token(id)
	if e != nil {
		t.Fatal(e)
	}
	if other, e := s.Token(testObject(99)); e != nil || other.Object != 0 || other.Collection != 0 {
		t.Fatalf("unrelated object %+v %v", other, e)
	}
	var history [][]byte
	if e = s.History(context.Background(), id, at.Add(-time.Second), at.Add(time.Second), func(_ uint64, b []byte) error { history = append(history, append([]byte(nil), b...)); return nil }); e != nil || len(history) != 1 || !bytes.Equal(history[0], testFrame(t, l, at, 2, false)) {
		t.Fatalf("history %v %v", history, e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if recovered, e := s.Token(id); e != nil || recovered != after {
		t.Fatalf("state recovery %+v %v", recovered, e)
	}
	loc, e = s.ObjectLocations(id)
	if e != nil || len(loc) != 1 {
		t.Fatalf("history recovery %+v %v", loc, e)
	}
	if _, e = s.Append(Error, testFrame(t, l, time.Now(), 10, false)); e != nil {
		t.Fatal(e)
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	if current, e := s.StillCurrent(id, after); e != nil || current {
		t.Fatalf("concurrent invalidation lost: %v %v", current, e)
	}
}

func TestObjectLookupRequiresIndex(t *testing.T) {
	for _, variable := range []bool{false, true} {
		t.Run(map[bool]string{false: "fixed", true: "variable"}[variable], func(t *testing.T) {
			l := testLayout(t, variable)
			at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
			s, err := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "owner", Layout: l, BatchRecords: 1})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			id, other := testObject(42), testObject(99)
			if variable {
				id, other = testObject(42), testObject(99)
			}
			query := Query{Branch: Write, From: at.Add(-time.Second), To: at.Add(time.Second), Equals: map[string]Value{"id": id}}
			reject := func() {
				t.Helper()
				visit := func(uint64, []byte) error { t.Fatal("disabled object lookup returned an event"); return nil }
				if err := s.History(context.Background(), id, query.From, query.To, visit); !errors.Is(err, ErrObjectIndexDisabled) {
					t.Fatalf("disabled history: %v", err)
				}
				if err := s.Search(context.Background(), query, visit); !errors.Is(err, ErrObjectIndexDisabled) {
					t.Fatalf("disabled object predicate: %v", err)
				}
				if _, err := s.ObjectLocations(id); !errors.Is(err, ErrObjectIndexDisabled) {
					t.Fatalf("disabled locations: %v", err)
				}
			}
			reject() // Even an empty collection must report that lookup is disabled.
			newer := testFrame(t, l, at.Add(time.Millisecond), 1, false)
			older := testFrame(t, l, at, 3, true)
			var f Frame
			if err := l.Scan(testFrame(t, l, at.Add(-time.Millisecond), 2, false), &f); err != nil {
				t.Fatal(err)
			}
			f.Values[l.Object] = other
			unrelated, err := l.Encode(nil, f.Values)
			if err != nil {
				t.Fatal(err)
			}
			for _, raw := range [][]byte{newer, unrelated, older} {
				if _, err := s.Append(Write, raw); err != nil {
					t.Fatal(err)
				}
			}
			reject()
			if got := collect(t, s, Write, query.From, query.To); len(got) != 3 {
				t.Fatal("time-only search must remain available without collection.idx")
			}
			config := filepath.Join(s.root, "registry.cfg")
			if err := os.WriteFile(config, []byte("version=1\n[defaults]\nobject_index=true\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := s.ReloadPolicy(); err != nil {
				t.Fatal(err)
			}
			for _, history := range []bool{false, true} {
				var got [][]byte
				visit := func(_ uint64, raw []byte) error { got = append(got, append([]byte(nil), raw...)); return nil }
				if history {
					err = s.History(context.Background(), id, query.From, query.To, visit)
				} else {
					err = s.Search(context.Background(), query, visit)
				}
				if err != nil || !reflect.DeepEqual(got, [][]byte{older, newer}) {
					t.Fatalf("indexed lookup history=%v: %d events, %v", history, len(got), err)
				}
			}
			query.From = at.Add(time.Nanosecond)
			var count int
			if err := s.Search(context.Background(), query, func(_ uint64, raw []byte) error {
				count++
				if !bytes.Equal(raw, newer) {
					t.Fatal("object lookup ignored the precise time bound")
				}
				return nil
			}); err != nil || count != 1 {
				t.Fatalf("object lookup time bound: %d %v", count, err)
			}
			if err := os.WriteFile(config, []byte("version=1\n[defaults]\nobject_index=false\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := s.ReloadPolicy(); err != nil {
				t.Fatal(err)
			}
			reject() // An existing postings file must not bypass the disabled policy.
		})
	}
}

func TestConfigOverridesAndInvalidReload(t *testing.T) {
	c, e := ParseConfiguration([]byte("version=1\ntimezone='America/New_York'\n[defaults.retention]\nread='7d'\nwrite='30d'\n[resources.\"payment/transactions.v1\"]\nobject_index=true\n[resources.\"payment/transactions.v1\".retention]\nread='3d'\n"))
	if e != nil {
		t.Fatal(e)
	}
	p := c.Resolve("payment/transactions.v1")
	dur, e := p.Retention.Durations()
	if e != nil || !(*p.ObjectIndex) || dur[Read] != 72*time.Hour || dur[Write] != 720*time.Hour {
		t.Fatalf("policy %+v %v", p, e)
	}
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "owner", Layout: testLayout(t, false)})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	old := s.policy
	e = os.WriteFile(filepath.Join(s.root, "registry.cfg"), []byte("version=999\n"), 0600)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.ReloadPolicy(); e == nil || !reflect.DeepEqual(old, s.policy) {
		t.Fatal("invalid policy replaced active policy")
	}
}

func TestRollupsAndStatistics(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	now := time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC)
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "owner", Layout: l, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	for _, b := range []Branch{Read, Write, Error, Debug, Trace} {
		if _, e = s.Append(b, testFrame(t, l, at, 1, false)); e != nil {
			t.Fatal(e)
		}
	}
	for _, r := range []Resolution{Daily, Weekly, Monthly, Yearly} {
		if e = s.PublishRollup(Write, r, at); e != nil {
			t.Fatal(e)
		}
		if e = s.PublishRollup(Write, r, at); e != nil {
			t.Fatal(e)
		}
		groups, e := s.ReadRollup(Write, r, at)
		if e != nil {
			t.Fatal(e)
		}
		var a Aggregate
		for _, g := range groups {
			a.merge(g)
		}
		if a.Count != 1 || a.Sum != 11 {
			t.Fatalf("rollup %+v", a)
		}
		if e = s.PublishRollup(Debug, r, at); e == nil {
			t.Fatal("debug rollup accepted")
		}
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	stats, e := s.Statistics()
	if e != nil {
		t.Fatal(e)
	}
	for _, b := range stats.Branches {
		if b.Events != 0 {
			t.Fatal("30-day expiry ignored")
		}
	}
	now = at.Add(time.Hour)
	stats, e = s.Statistics()
	if e != nil || stats.Branches["debug"].Events != 1 || stats.Branches["write"].Cardinality["state"] != 1 {
		t.Fatalf("stats %+v %v", stats, e)
	}
}

func TestReferenceCapacityKeepsLiteral(t *testing.T) {
	l := testLayout(t, false)
	d := l.Definition
	d.Fields[4] = Field{Name: "state", Type: "U16"}
	var e error
	l, e = Compile(d)
	if e != nil {
		t.Fatal(e)
	}
	tr := newTrie()
	v := make([]Value, len(l.Widths))
	for i := 0; i < 65535; i++ {
		v[4] = Uint(uint64(i), 2)
		ref := tr.intern(l, v)
		if ref != uint16(i+1) {
			t.Fatalf("reference %d %d", i, ref)
		}
	}
	v[4] = Uint(65535, 2)
	if ref := tr.intern(l, v); ref != 0 || len(tr.nodes) != 65536 {
		t.Fatalf("capacity overflow %d %d", ref, len(tr.nodes))
	}
	if v[4].State != Direct || unsigned(v[4].Bytes) != 65535 {
		t.Fatal("literal lost")
	}
	v[4] = Value{}
	if ref := tr.intern(l, v); ref != 0 {
		t.Fatal("absent root referenced")
	}
}

func TestRetentionPreservesRowsAndInvalidatesReplay(t *testing.T) {
	l := testLayout(t, true)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "owner", Layout: l, ObjectIndex: true, Now: func() time.Time { return now }}
	s, e := Open(c)
	if e != nil {
		t.Fatal(e)
	}
	id := testObject(42)
	old := testFrame(t, l, now.Add(-48*time.Hour), 1, false)
	kept := testFrame(t, l, now.Add(-2*time.Hour), 2, true)
	for _, frame := range [][]byte{old, kept} {
		if _, e = s.Append(Write, frame); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = s.Append(Error, old); e != nil {
		t.Fatal(e)
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	before, _ := s.Token(id)
	generation := s.Generation(Write)
	if s.generationSequence(Write) != 1 {
		t.Fatal("initial generation sequence")
	}
	raw := []byte("version=1\ntimezone='America/New_York'\n[defaults]\nobject_index=true\n[defaults.retention]\nwrite='1d'\nerror='1d'\n")
	if e = os.WriteFile(filepath.Join(s.root, "registry.cfg"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	if e = s.ReloadPolicy(); e != nil {
		t.Fatal(e)
	}
	if e = s.ApplyRetention(); e != nil {
		t.Fatal(e)
	}
	if generation != s.Generation(Write) {
		t.Fatal("retention changed source identity")
	}
	if s.generationSequence(Write) != 1 || s.generationSequence(Error) != 1 || s.generationSequence(Read) != 1 {
		t.Fatal("generation sequences did not follow branch retention", s.catalog.GenerationSequences)
	}
	rows := collect(t, s, Write, now.Add(-72*time.Hour), now)
	if len(rows) != 1 || !bytes.Equal(rows[0], kept) {
		t.Fatal("retained event changed")
	}
	locations, e := s.ObjectLocations(id)
	if e != nil || len(locations) != 1 || locations[0].Row != 3 {
		t.Fatalf("rebuilt objects %+v %v", locations, e)
	}
	if rows := collect(t, s, Error, now.Add(-72*time.Hour), now); len(rows) != 0 {
		t.Fatal("error retention ignored")
	}
	if token, e := s.Token(id); e != nil || token.Collection <= before.Collection {
		t.Fatalf("write retention failed to invalidate replay: %+v %v", token, e)
	}
	if _, e = os.Stat(filepath.Join(s.dir, "write.idx")); !os.IsNotExist(e) {
		t.Fatalf("old generation not removed: %v", e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if s.generationSequence(Write) != 1 || s.generationSequence(Error) != 1 {
		t.Fatal("generation sequences lost on restart")
	}
	rows = collect(t, s, Write, now.Add(-72*time.Hour), now)
	if len(rows) != 1 || !bytes.Equal(rows[0], kept) {
		t.Fatal("generation reopening failed")
	}
	locations, e = s.ObjectLocations(id)
	if e != nil || len(locations) != 1 {
		t.Fatalf("reopened objects %+v %v", locations, e)
	}
}

func TestRetentionCleansDisabledObjectIndex(t *testing.T) {
	l := testLayout(t, false)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "owner", Layout: l, ObjectIndex: true, Now: func() time.Time { return now }}
	s, e := Open(c)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Append(Write, testFrame(t, l, now.Add(-48*time.Hour), 1, false)); e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	config := filepath.Join(c.Root, c.Cache, "registry.cfg")
	if e = os.WriteFile(config, []byte("version=1\ntimezone='America/New_York'\n[defaults]\nobject_index=false\n[defaults.retention]\nwrite='1d'\n"), 0600); e != nil {
		t.Fatal(e)
	}
	c.ObjectIndex = false
	s, e = Open(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if s.objects != nil {
		t.Fatal("disabled index was loaded")
	}
	if e = s.ApplyRetention(); e != nil {
		t.Fatal(e)
	}
	if s.objects != nil {
		t.Fatal("maintenance retained disabled index directory")
	}
	f, e := os.Open(filepath.Join(s.collection, "collection.idx"))
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	index, err := loadObjectIndex(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.sources) > 0 && len(index.sources[objectBinding{s.layout.Definition.Resource.Minor, s.branches[Write].generation}].Fences) != 0 {
		t.Fatal("expired object postings remain")
	}
}
