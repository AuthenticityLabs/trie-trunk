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

func TestStoredHourlyPACRecoveryAndDelivery(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	now := at.Add(3 * time.Hour)
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }, BatchRecords: 3}
	s, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.collection, "error.cfg"), []byte("version=1\n[policy.pac]\ngzip_level=9\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.ReloadPolicy(); err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 8; i++ {
		_, err = s.Append(Error, testFrame(t, l, at.Add(time.Duration(i)*time.Minute), i+1, false))
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Flush(); err != nil {
		t.Fatal(err)
	}
	b := s.branches[Error]
	if len(b.runs) != 1 {
		t.Fatalf("bounded hour has %d blocks", len(b.runs))
	}
	var cursor uint64
	for n, run := range b.runs {
		if run.PAC == nil || !run.PAC.Gzip || run.PAC.Placement.MemberStart != cursor+4+4*(run.End-run.Start) ||
			run.PAC.Placement.Block != uint32(n) {
			t.Fatal("invalid block directory")
		}
		cursor += 4 + run.PAC.Placement.MemberBytes
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	events := collect(t, s, Error, at, at.Add(time.Hour))
	if len(events) != 8 {
		t.Fatalf("recovered %d events", len(events))
	}
	var manifest Manifest
	var stored [][]byte
	err = s.Collect(context.Background(), CollectionRequest{Branch: Error, At: at},
		func(m Manifest) error { manifest = m; return nil },
		func(c NativeChunk) error {
			if c.Kind == "pac" {
				run := s.branches[Error].runs[c.PAC.Placement.Block]
				disk, e := readBytes(s.branches[Error].pods, run.PodsStart, run.PodsEnd)
				if e != nil {
					return e
				}
				if !bytes.Equal(disk, c.Data) {
					t.Fatal("delivery reencoded stored bytes")
				}
				stored = append(stored, c.Data)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Provisional || len(manifest.Blocks) != 1 || len(stored) != 1 {
		t.Fatal("incomplete hour manifest")
	}
	request := httptest.NewRequest("GET", "/collect?format=native&tap=demo/payment/transactions.v1/error&from="+at.Format(time.RFC3339)+"&to="+at.Add(time.Hour).Format(time.RFC3339), nil)
	request.Header.Set("Accept", "application/pil")
	request.Header.Set("Accept", "application/pil")
	request.Header.Set("PIL-Version", "2")
	request.Header.Set("PIL-Features", "native-ranges,native-state-pages,adaptive-integers")
	request.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	(&HTTP{Store: s}).nativeCollect(recorder, request)
	if recorder.Code != 200 {
		t.Fatal(recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Encoding") != "" {
		t.Fatal("PAC compression is not HTTP content encoding")
	}
	decoded := recorder.Body.Bytes()
	if !bytes.HasSuffix(decoded, []byte{255, 254}) {
		t.Fatal("missing success marker")
	}
	for _, raw := range stored {
		if !bytes.Contains(decoded, raw) {
			t.Fatal("delivery altered stored PAC")
		}
		if _, err := expandPAC(raw); err != nil {
			t.Fatal(err)
		}
	}
	var e error

	if os.Getenv("UPDATE_PAC_STORAGE_FIXTURE") == "1" {
		dir := "../../../client/services/fixtures/pac-storage"
		if e = os.MkdirAll(dir, 0755); e != nil {
			t.Fatal(e)
		}
		for name, data := range map[string][]byte{"hour.pil": decoded} {
			if e = os.WriteFile(filepath.Join(dir, name), data, 0644); e != nil {
				t.Fatal(e)
			}
		}
	}
}

func TestHourlyPACWaitsForClosingGrace(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 19, 12, 20, 0, 0, time.UTC)
	now := at.Add(20 * time.Minute)
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = s.Append(Write, testFrame(t, l, at, 1, false)); e != nil {
		t.Fatal(e)
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	if len(s.branches[Write].rows) != 0 {
		t.Fatal("open hour committed")
	}
	now = time.Date(2026, 10, 19, 13, 10, 0, 0, time.UTC)
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	if len(s.branches[Write].rows) != 0 {
		t.Fatal("closing hour committed before grace")
	}
	if _, e = s.Append(Write, testFrame(t, l, at.Add(time.Minute), 2, true)); e != nil {
		t.Fatal(e)
	}
	if os.Getenv("UPDATE_PAC_STORAGE_FIXTURE") == "1" {
		for _, mode := range []string{"native", "live"} {
			url := "/collect?tap=demo/payment/transactions.v1/write&from=2026-10-19T12:00:00Z&to=2026-10-19T13:00:00Z"
			if mode == "native" {
				url += "&format=native"
			}
			req := httptest.NewRequest("GET", url, nil)
			req.Header.Set("Accept", "application/pil")
			req.Header.Set("PIL-Version", "2")
			req.Header.Set("PIL-Features", "native-ranges,native-state-pages,adaptive-integers")
			req.Header.Set("Accept-Encoding", "gzip")
			w := httptest.NewRecorder()
			if mode == "live" {
				benchmarkStandaloneCollect(w, req, &HTTP{Store: s})
			} else {
				(&HTTP{Store: s}).ServeHTTP(w, req)
			}
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			raw := w.Body.Bytes()
			if w.Header().Get("Content-Encoding") == "gzip" {
				raw = ungzipTest(t, raw)
			}
			if e := os.WriteFile("../../../client/services/fixtures/pac-storage/"+mode+"-open.pil", raw, 0644); e != nil {
				t.Fatal(e)
			}
		}
	}
	now = now.Add(5 * time.Minute)
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	if len(s.branches[Write].rows) != 3*IndexRowSize {
		t.Fatal("sealed hour missing late event")
	}
	if s.branches[Write].runs[0].PAC.Gzip {
		t.Fatal("write packs must remain uncompressed")
	}
}

func TestPACRetirementPreservesReferencesAndCompressedBytes(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, ObjectIndex: true, Now: func() time.Time { return at.Add(72 * time.Hour) }}
	s, e := Open(cfg)
	if e != nil {
		t.Fatal(e)
	}
	for _, branch := range []Branch{Write, Error} {
		for i := 0; i < 3; i++ {
			if _, e = s.Append(branch, testFrame(t, l, at.Add(time.Duration(i)*time.Hour), uint32(i+1), false)); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	b := s.branches[Error]
	source := b.generation
	run := b.runs[2]
	before, e := readBytes(b.pods, run.PodsStart, run.PodsEnd)
	if e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	e = s.prune(Error, at.Add(2*time.Hour))
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	b = s.branches[Error]
	if b.generation != source || b.baseRows != 4 || b.blockBase != 2 || b.tail() != 6 {
		t.Fatal("retirement changed reference identity")
	}
	after, e := readBytes(b.pods, run.PodsStart-b.podsRetired, run.PodsEnd-b.podsRetired)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("retention recompressed survivors")
	}
	if n, e := retiredBytes(b.idx); e != nil || n != 4*IndexRowSize {
		t.Fatal("missing index retirement header", n, e)
	}
	if len(collect(t, s, Error, at, at.Add(4*time.Hour))) != 1 {
		t.Fatal("retired rows still searchable")
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if len(collect(t, s, Error, at, at.Add(4*time.Hour))) != 1 {
		t.Fatal("retirement lost across restart")
	}
	s.mu.Lock()
	e = s.prune(Error, at.Add(3*time.Hour))
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	if s.branches[Error].tail() != 6 || len(s.branches[Error].rows) != 0 {
		t.Fatal("empty retired index reset tail")
	}
}

// Exercise continuous member coordinates across hour and page boundaries.
// The highly compressible fixture is intentionally large after decoding.
func TestPACCrossPageGapFixtures(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(72 * time.Hour) }}
	s, e := Open(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	for i := 0; i < 6; i++ {
		v := []Value{uuid(at.Add(time.Duration(i/2)*time.Hour+time.Duration(i%2)*time.Minute).UnixMilli(), uint32(i+1)), testObject(42), Uint(uint64(i+1), 8), Uint(20, 8), Uint(0, 1), Text(string(bytes.Repeat([]byte{byte('a' + i)}, 800_003))), Uint(999, 8)}
		raw, e := l.Encode(nil, v)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.Append(Error, raw); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	b := s.branches[Error]
	if len(b.runs) != 3 {
		t.Fatal("expected three hourly PACs", len(b.runs))
	}
	for i, r := range b.runs {
		if r.PAC.Placement.FirstRow != uint64(i*3+1) || i > 0 && r.PAC.Placement.MemberStart%NativePageSize == 0 {
			t.Fatal("unexpected hour padding")
		}
	}
	deliver := func(name string, hour int) {
		t.Helper()
		start := at.Add(time.Duration(hour) * time.Hour)
		request := httptest.NewRequest("GET", "/collect?format=native&tap=demo/payment/transactions.v1/error&from="+start.Format(time.RFC3339)+"&to="+start.Add(time.Hour).Format(time.RFC3339), nil)
		request.Header.Set("Accept", "application/pil")
		request.Header.Set("PIL-Version", "2")
		request.Header.Set("PIL-Features", "native-ranges,native-state-pages,adaptive-integers")
		request.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		(&HTTP{Store: s}).nativeCollect(w, request)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		readNativeTest(t, w.Body.Bytes())
		if os.Getenv("UPDATE_PAC_STORAGE_FIXTURE") == "1" {
			if e := os.WriteFile("../../../client/services/fixtures/pac-storage/"+name+".pil", w.Body.Bytes(), 0644); e != nil {
				t.Fatal(e)
			}
		}
	}
	for i := 0; i < 3; i++ {
		deliver(fmt.Sprintf("gap-%d", i), i)
	}
	s.mu.Lock()
	e = s.prune(Error, at.Add(2*time.Hour))
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	deliver("retired", 2)
	s.mu.Lock()
	e = s.prune(Error, at.Add(3*time.Hour))
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(cfg)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Append(Error, testFrame(t, l, at.Add(72*time.Hour), 7, false)); e != nil {
		t.Fatal(e)
	}
	// Advance the clock so the new hour can finalize after the empty restart.
	s.config.Now = func() time.Time { return at.Add(74 * time.Hour) }
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	if s.branches[Error].baseRows != 9 || s.branches[Error].tail() != 11 {
		t.Fatal("retired source ordinal reset")
	}
	deliver("after-retirement", 72)
}
