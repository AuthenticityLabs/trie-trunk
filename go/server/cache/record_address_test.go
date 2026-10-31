package cache

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestRecordStartsAcrossHeadersPagesAndRetention(t *testing.T) {
	const base = int64(1792584000000)
	b := branchStore{rows: make([]byte, 261*IndexRowSize)}
	b.runs = []Run{
		{Start: 1, End: 258, PAC: &StoredPAC{Placement: PACPlacement{From: base, To: base + TimeHourMS, MemberStart: 4, MemberBytes: 257 * 37}}},
		{Start: 259, End: 261, PAC: &StoredPAC{Placement: PACPlacement{From: base + TimeHourMS, To: base + 2*TimeHourMS, MemberStart: 4 + 257*37 + 4, MemberBytes: 2 * 37}}},
	}
	for _, r := range b.runs {
		copy(b.row(r.Start-1), indexHeader(r.PAC.Placement.From, r.End-r.Start))
		for n := r.Start; n < r.End; n++ {
			order.PutUint32(b.row(n), uint32(r.PAC.Placement.From+int64(n-r.Start)))
		}
	}
	check := func() {
		for _, r := range b.runs {
			if e := validateIndexHeader(b.row(r.Start-1), r); e != nil {
				t.Fatal(e)
			}
			for n := r.Start; n < r.End; n++ {
				if b.rowTime(n) != r.PAC.Placement.From+int64(n-r.Start) {
					t.Fatal("start boundary", n)
				}
			}
		}
	}
	check()
	b.rows = b.rows[258*IndexRowSize:]
	b.baseRows = 258
	b.runs = b.runs[1:]
	check()
}

func TestRecordAddressRejectsCorruptHeader(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(72 * time.Hour) }}
	s, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Append(Error, testFrame(t, l, at, 1, false)); err != nil {
		t.Fatal(err)
	}
	if err = s.Flush(); err != nil {
		t.Fatal(err)
	}
	name := s.branches[Error].idx.Name()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	raw[headerSize+4] = 9
	if err = os.WriteFile(name, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s, err = Open(cfg)
	if err == nil {
		s.Close()
		t.Fatal("corrupt header accepted on restart")
	}
}

func TestRecordAddressHeaderAndRetention(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(72 * time.Hour) }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	for i := 0; i < 3; i++ {
		if _, e = s.Append(Error, testFrame(t, l, at.Add(time.Duration(i)*time.Hour), uint32(i+1), false)); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	b := s.branches[Error]
	for _, r := range b.runs {
		if e = validateIndexHeader(b.row(r.Start-1), r); e != nil {
			t.Fatal(e)
		}
		for n := r.Start; n < r.End; n++ {
			if got, e := b.searchRow(b.searchRef(n)); e != nil || got != n {
				t.Fatal(got, e)
			}
		}
		if _, e = b.searchRow(uint64(b.eventReference(r.Start).Hour) << 32); e == nil {
			t.Fatal("header accepted as event")
		}
	}
	last := b.runs[2]
	ref := b.searchRef(last.Start)
	row := bytes.Clone(b.row(last.Start))
	s.mu.Lock()
	e = s.prune(Error, at.Add(2*time.Hour))
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	b = s.branches[Error]
	if !bytes.Equal(row, b.row(last.Start)) {
		t.Fatal("retention moved reference")
	}
	if got, e := b.searchRow(ref); e != nil || got != last.Start {
		t.Fatal(got, e)
	}
	if _, e = b.searchRow(1); e == nil {
		t.Fatal("retired reference accepted")
	}
	if _, e = b.searchRow(IndexCapacity); e == nil {
		t.Fatal("overflow accepted")
	}
}

func TestOptionalSharedReferenceInPIL(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprint(shared), func(t *testing.T) {
			d := testLayout(t, false).Definition
			if shared {
				d.Fields = append(d.Fields, Field{Name: "classification_ref", Type: "REF16"})
				d.Resource.Roles.Shared = "classification_ref"
			}
			l, e := Compile(d)
			if e != nil {
				t.Fatal(e)
			}
			at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
			s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(72 * time.Hour) }})
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			base := testLayout(t, false)
			original := testFrame(t, base, at, 1, false)
			var f Frame
			if e = base.Scan(original, &f); e != nil {
				t.Fatal(e)
			}
			if shared {
				f.Values = append(f.Values, Value{})
			}
			original, e = l.Encode(nil, f.Values)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.Append(Error, original); e != nil {
				t.Fatal(e)
			}
			if e = s.Flush(); e != nil {
				t.Fatal(e)
			}
			b := s.branches[Error]
			var reader podsReader
			stored, e := reader.read(b, b.runs[0].Start, s.row)
			if e != nil {
				t.Fatal(e)
			}
			if e = l.Scan(stored, &f); e != nil {
				t.Fatal(e)
			}
			state := fieldIndex(l, "state")
			if f.Values[state].State != Absent || f.Values[l.Leaf].State != Absent || shared && f.Values[l.Shared].State != Absent {
				t.Fatal("state or leaf duplicated in payload")
			}
			full, e := s.frame(b, b.runs[0].Start, nil)
			if e != nil || !sameSeedData(t, l, original, full) {
				t.Fatal("reference expansion round trip", e)
			}
		})
	}
}

func TestCapacityRolloverNegotiatesNewMinor(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, indexCapacity: 4, Now: func() time.Time { return at.Add(72 * time.Hour) }}
	p, e := OpenEndpoint(cfg)
	if e != nil {
		t.Fatal(e)
	}
	post := func(layout *Layout, raw []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/ingest?tap=demo/payment/transactions.v1/write", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/pil")
		r.Header.Set("PIL-Version", "2")
		r.Header.Set("Definition-Version", fmt.Sprintf("%x", layout.Digest))
		w := httptest.NewRecorder()
		p.serveHTTP(w, r)
		return w
	}
	raw := append(testFrame(t, l, at, 1, false), testFrame(t, l, at.Add(time.Millisecond), 2, false)...)
	raw = append(raw, testFrame(t, l, at.Add(2*time.Millisecond), 3, false)...)
	w := post(l, raw)
	if w.Code != 412 || w.Header().Get("Trunk-Received") != "2" || w.Header().Get("PIL-Capacity-Rollover") != "1" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	next := p.preferred[collectionKey(l)].Store.layout
	if next.Definition.Resource.Minor != l.Definition.Resource.Minor+1 {
		t.Fatal("no minor rollover")
	}
	// Same fields, new binding: no payload conversion is required for capacity rollover.
	if w = post(next, testFrame(t, l, at.Add(2*time.Millisecond), 3, false)); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	old := p.apis[fmt.Sprintf("%x", l.Digest)].Store
	if e = old.Flush(); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = old.Search(context.Background(), Query{Branch: Write, From: at, To: at.Add(time.Hour)}, func(_ uint64, _ []byte) error { count++; return nil }); e != nil || count != 2 {
		t.Fatal(count, e)
	}
	if e = p.Close(); e != nil {
		t.Fatal(e)
	}
	p, e = OpenEndpoint(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	if p.preferred[collectionKey(l)].Store.layout.Digest != next.Digest {
		t.Fatal("rollover preference lost")
	}
	if w = post(l, testFrame(t, l, at.Add(3*time.Millisecond), 4, false)); w.Code != 412 {
		t.Fatal("old minor reopened", w.Code, w.Body.String())
	}
}
