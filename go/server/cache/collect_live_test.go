package cache

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func collectionLivePacks(t testing.TB, raw []byte, l *Layout) []*PAC {
	t.Helper()
	if len(raw) < 7 || !bytes.Equal(raw[:5], []byte{'T', 'P', 'I', 'L', 2}) {
		t.Fatal("invalid collection stream")
	}
	var packs []*PAC
	at := 5
	for at < len(raw) {
		tag := raw[at]
		at++
		if tag == 255 {
			if at != len(raw)-1 || raw[at] != 254 {
				t.Fatal("incomplete collection")
			}
			return packs
		}
		if tag == 7 {
			size := int(order.Uint32(raw[at:]))
			p, e := ParsePAC(l, raw[at:at+size], map[uint32]bool{PACSource: true})
			if e != nil {
				t.Fatal(e)
			}
			packs = append(packs, p)
			at += size
			continue
		}
		if tag == 9 {
			size := int(order.Uint64(raw[at+4:]))
			at += 12 + size
			continue
		}
		var width []int
		switch tag {
		case 1:
			width = defineControl.Widths
		case 2:
			width = bindControl.Widths
		case 4:
			width = manifestControl.Widths
		case 5:
			width = chunkControl.Widths
		case 6:
			width = endControl.Widths
		case 11:
			width = liveWindowControl.Widths
		default:
			t.Fatalf("unexpected tag %d", tag)
		}
		scanner := *defineControl
		scanner.Widths = width
		frame, e := scanner.ReadFrame(bytes.NewReader(raw[at:]), nil)
		if e != nil {
			t.Fatal(e)
		}
		at += len(frame)
	}
	t.Fatal("missing completion")
	return nil
}
func TestCollectLiveWindow(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	now := at.Add(10 * time.Minute)
	p, e := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	add := func(ts time.Time, n uint32) {
		t.Helper()
		if _, e := p.api.Store.Append(Write, testFrame(t, l, ts, n, false)); e != nil {
			t.Fatal(e)
		}
	}
	fixture := os.Getenv("TRUNK_COLLECTION_FIXTURES")
	number := 0
	if fixture != "" {
		if e := os.MkdirAll(fixture, 0755); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(fixture, "definition.rx"), l.Bytes, 0644); e != nil {
			t.Fatal(e)
		}
	}
	pull := func(from time.Time) []*PAC {
		t.Helper()
		r := httptest.NewRequest("GET", "/collect?tap=*/*/*/*&from="+from.Format(time.RFC3339Nano)+"&to=now", nil)
		r.Header.Set("Accept", "application/pil")
		r.Header.Set("PIL-Version", "2")
		r.Header.Set("PIL-Features", "native-ranges,live-pacs,adaptive-integers")
		r.Header.Set("Definition-Known", fmtDigestNative(l.Digest))
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if w.Header().Get("Trunk-Window-To") != fmt.Sprint(now.UnixMilli()) {
			t.Fatal("incorrect upper bound")
		}
		if fixture != "" {
			if e := os.WriteFile(filepath.Join(fixture, fmt.Sprintf("%d.pil", number)), w.Body.Bytes(), 0644); e != nil {
				t.Fatal(e)
			}
		}
		number++
		return collectionLivePacks(t, w.Body.Bytes(), l)
	}
	count := func(packs []*PAC) int {
		n := 0
		for _, p := range packs {
			n += len(p.Members)
			if p.Headers["seed.hour"] == nil || len(p.Headers["seed.rows"]) != len(p.Members)*6 {
				t.Fatal("missing Seed headers")
			}
		}
		return n
	}
	add(at.Add(30*time.Second), 10)
	add(at.Add(time.Minute), 1)
	if count(pull(at)) != 2 {
		t.Fatal("initial live event missing")
	}
	cp := at.Add(time.Minute - time.Millisecond)
	now = now.Add(time.Second)
	if count(pull(cp)) != 1 {
		t.Fatal("overlap must follow the requested time range")
	}
	add(at.Add(11*time.Minute), 2)
	now = at.Add(12 * time.Minute)
	packs := pull(cp)
	if count(packs) != 2 || len(packs[0].Common) == 0 {
		t.Fatal("missing factored live pack")
	}
	for _, member := range packs[0].Members {
		raw, e := ResolvePAC(l, packs[0].Common, member)
		if e != nil {
			t.Fatal(e)
		}
		var f Frame
		if e = l.Scan(raw, &f); e != nil {
			t.Fatal(e)
		}
		if f.Values[l.Event].State != Absent || f.Values[l.Time].State != Absent {
			t.Fatal("redundant identity/time in pack members")
		}
	}
	// An ordinary time range never silently rewinds to include older arrivals.
	add(at.Add(15*time.Second), 3)
	if count(pull(cp)) != 2 {
		t.Fatal("collection ignored its lower bound")
	}
	if count(pull(at)) != 4 {
		t.Fatal("wider range did not return the older arrival")
	}
	now = at.Add(2 * time.Hour)
	if e = p.api.Store.Flush(); e != nil {
		t.Fatal(e)
	}
	if count(pull(cp)) != 0 {
		t.Fatal("sealed rows were also returned as live")
	}
}
func TestLivePackKeepsProvisionalIdentityAndTime(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(time.Minute) }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	for i := uint32(1); i <= 3; i++ {
		if _, e = s.Append(Write, testFrame(t, l, at.Add(time.Duration(i)*time.Millisecond), i, false)); e != nil {
			t.Fatal(e)
		}
	}
	live, e := s.liveWindow(Write, at, at.Add(time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	h := &HTTP{Store: s}
	if e = h.writeLiveWindow(context.Background(), &out, 0, Write, live); e != nil {
		t.Fatal(e)
	}
	p, e := s.livePack(Write, at.UnixMilli(), live.events)
	if e != nil {
		t.Fatal(e)
	}
	for i, m := range p.Members {
		raw, e := ResolvePAC(l, p.Common, m)
		if e != nil {
			t.Fatal(e)
		}
		var frame Frame
		if e = l.Scan(raw, &frame); e != nil {
			t.Fatal(e)
		}
		if unsigned(frame.Values[l.Record].Bytes) != uint64(i) {
			t.Fatal("provisional ordinal changed")
		}
		if order.Uint32(p.Headers["seed.rows"][i*6:]) != uint32(at.UnixMilli()+int64(i+1)) {
			t.Fatal("time changed")
		}
	}
	t.Logf("three live rows: %d PAC bytes", len(p.Bytes))
}

func TestCollectLivePromotionBehindStart(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	now := at.Add(65 * time.Minute)
	p, e := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	for i, ts := range []time.Time{at.Add(59*time.Minute + 50*time.Second), at.Add(61 * time.Minute)} {
		if _, e = p.api.Store.Append(Write, testFrame(t, l, ts, uint32(i+1), false)); e != nil {
			t.Fatal(e)
		}
	}
	pull := func(name string, from time.Time, to string, live bool) []*PAC {
		t.Helper()
		r := httptest.NewRequest("GET", "/collect?tap="+p.api.tap(Write)+"&from="+from.Format(time.RFC3339Nano)+"&to="+to, nil)
		r.Header.Set("Accept", "application/pil")
		r.Header.Set("PIL-Version", "2")
		features := "native-ranges,adaptive-integers"
		if live {
			features += ",live-pacs"
		}
		r.Header.Set("PIL-Features", features)
		r.Header.Set("Definition-Known", fmtDigestNative(l.Digest))
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if folder := os.Getenv("TRUNK_COLLECTION_FIXTURES"); folder != "" {
			if e = os.WriteFile(filepath.Join(folder, name+".pil"), w.Body.Bytes(), 0644); e != nil {
				t.Fatal(e)
			}
		}
		return collectionLivePacks(t, w.Body.Bytes(), l)
	}
	if packs := pull("promotion-0", at, "now", true); len(packs) != 2 {
		t.Fatal("expected two open hours")
	}
	now = at.Add(80 * time.Minute)
	if packs := pull("promotion-1", at.Add(61*time.Minute-time.Millisecond), "now", true); len(packs) != 1 || order.Uint64(packs[0].Headers["seed.hour"]) != uint64(at.Add(time.Hour).UnixMilli()) {
		t.Fatal("diff should contain only the newer open hour")
	}
	if packs := pull("promotion-2", at, at.Add(time.Hour).Format(time.RFC3339Nano), false); len(packs) != 0 {
		t.Fatal("sealed promotion must use native PAC ranges")
	}
}
