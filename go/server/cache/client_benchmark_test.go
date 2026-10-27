package cache

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in reproducible workload fixture for real browser codec/OPFS benchmarks.
func TestClientHourlyBenchmarkFixture(t *testing.T) {
	out := os.Getenv("TRUNK_CLIENT_BENCH_OUTPUT")
	if out == "" {
		t.Skip("set TRUNK_CLIENT_BENCH_OUTPUT to generate the million-event fixture")
	}
	const hours = 256
	const perHour = 4096
	l := testLayout(t, false)
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	now := at.Add(2 * time.Hour)
	s, err := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }, NativeMemoryBytes: 32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Full-text index construction is outside this native collection benchmark.
	off := false
	s.policy.SearchIndex = &off
	s.mu.Lock()
	b, err := s.openBranch(Error)
	if err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	// Prepare canonical sealed hours directly. Ingress and full-text indexing
	// are not part of the client decoder/writer measurement.
	for hour := 0; hour < hours; hour++ {
		now = at.Add(time.Duration(hour+2) * time.Hour)
		batch := make([]pending, perHour)
		for n := 0; n < perHour; n++ {
			stamp := at.Add(time.Duration(hour)*time.Hour + time.Duration(n)*time.Millisecond)
			ordinal := uint32(hour*perHour + n + 1)
			raw := testFrame(t, l, stamp, ordinal, false)
			id, e := eventID(uuid(stamp.UnixMilli(), ordinal))
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			var frame Frame
			if err = l.Scan(raw, &frame); err != nil {
				s.mu.Unlock()
				t.Fatal(err)
			}
			if err = s.accumulate(b, id, frame.Values); err != nil {
				s.mu.Unlock()
				t.Fatal(err)
			}
			batch[n] = pending{id, raw}
			b.pendingBytes += len(raw)
		}
		if err = s.commitBatch(b, batch); err != nil {
			s.mu.Unlock()
			t.Fatal(err)
		}
		b.finalizedBefore = at.Add(time.Duration(hour+1) * time.Hour).UnixMilli()
		b.pruneMemory(now)
		if (hour+1)%16 == 0 {
			t.Logf("sealed %d hours", hour+1)
		}
	}
	if err = s.publishTime(b); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()
	r := httptest.NewRequest("GET", "/collect?format=native&tap=demo/payment/transactions.v1/error&from="+at.Format(time.RFC3339)+"&to="+at.Add(hours*time.Hour).Format(time.RFC3339), nil)
	r.Header.Set("Accept", "application/pil")
	r.Header.Set("PIL-Version", "2")
	r.Header.Set("PIL-Features", "native-ranges,native-state-pages,adaptive-integers")
	w := httptest.NewRecorder()
	(&HTTP{Store: s}).nativeCollect(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err = os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(out, w.Body.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	meta := fmt.Sprintf(`{"tap":"demo/payment/transactions.v1/error","from":%q,"to":%q,"headers":{"Content-Type":"application/pil","PIL-Version":"2","Trunk-Format":"native-pac/0","Trunk-Completion":"body","Definition-Version":%q},"hours":%d,"events":%d,"wireBytes":%d}`, at.Format(time.RFC3339), at.Add(hours*time.Hour).Format(time.RFC3339), w.Header().Get("Definition-Version"), hours, hours*perHour, w.Body.Len())
	if err = os.WriteFile(out+".json", []byte(meta), 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d hours, %d events, %d wire bytes", hours, hours*perHour, w.Body.Len())
}
