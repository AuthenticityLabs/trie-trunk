package cache

import (
	"bytes"
	"encoding/json"
	"errors"
	pil "github.com/AuthenticityLabs/pill-packer/go/codec"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestDailySummaryCodec(t *testing.T) {
	sum := aggregateFixture()
	sum.Statistics = []statisticsGroup{
		{Period: 0, Labels: [4]string{"payment", "transactions.v1", "read", "INFO"}, Count: 5, Durations: [][2]uint64{{4, 1}, {6, 1}}},
		{Period: sumBucketMS, Labels: [4]string{"payment", "transactions.v1", "read", "ERROR"}, Count: 1, Errors: 1, Durations: [][2]uint64{{0, 1}}},
	}
	for _, compressed := range []bool{false, true} {
		raw, e := encodeSummaryDay(0, 0, summaryDayMS, []Summary{sum}, compressed)
		if e != nil {
			t.Fatal(e)
		}
		day, from, to, got, e := decodeSummaryDay(raw)
		if e != nil || day != 0 || from != 0 || to != summaryDayMS || !reflect.DeepEqual(got, []Summary{sum}) {
			t.Fatalf("round trip: %v %+v", e, got)
		}
		for n := 0; n < len(raw); n++ {
			if _, _, _, _, e = decodeSummaryDay(raw[:n]); e == nil {
				t.Fatalf("truncation %d", n)
			}
		}
		for _, at := range []int{0, 8, 12, 16, 20, 24, 32, len(raw) - 1} {
			bad := bytes.Clone(raw)
			bad[at] ^= 255
			// The compressed PAC wrapper is 16 bytes. Gzip MTIME and XFL
			// metadata are outside its payload CRC and may change harmlessly.
			if compressed && (at == 20 || at == 24) {
				d, f, z, decoded, err := decodeSummaryDay(bad)
				if err != nil || d != day || f != from || z != to || !reflect.DeepEqual(decoded, got) {
					t.Fatalf("gzip metadata changed decoded summary at %d: %v", at, err)
				}
				continue
			}
			if _, _, _, _, e = decodeSummaryDay(bad); e == nil {
				t.Fatalf("corruption %d", at)
			}
		}
	}
	if _, e := encodeSummaryDay(0, 0, summaryDayMS, []Summary{{}}, true); e == nil {
		t.Fatal("empty chunk")
	}
	if _, e := encodeSummaryDay(0, 0, summaryDayMS, []Summary{sum, sum}, true); e == nil {
		t.Fatal("duplicate hour")
	}
	// A PAC needs at least one summary member to carry its day coverage.
	if _, e := encodeSummaryDay(0, TimeHourMS, 3*TimeHourMS, nil, true); e == nil {
		t.Fatal("empty summary PAC accepted")
	}
}

func TestDailySummaryRolloverRecoveryAndConcurrentIngress(t *testing.T) {
	l := testLayout(t, false)
	start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	now := start.Add(23*time.Hour + 30*time.Minute)
	ready, release := make(chan struct{}), make(chan struct{})
	blocked := false
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }, sealHook: func(stage string) error {
		if stage == "summary-day-ready" && !blocked {
			blocked = true
			close(ready)
			<-release
		}
		return nil
	}}
	s, e := Open(cfg)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 24; i++ {
		if _, e = s.Append(Write, testFrame(t, l, start.Add(time.Duration(i)*time.Hour), uint32(i+1), i%3 == 0)); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	if len(s.branches[Write].summaryDays) != 0 {
		t.Fatal("day packed before late-arrival cutoff")
	}
	now = start.Add(24*time.Hour + 15*time.Minute)
	done := make(chan error, 1)
	go func() { done <- s.Flush() }()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("no daily job")
	}
	appended := make(chan error, 1)
	go func() { _, e := s.Append(Write, testFrame(t, l, now, 100, false)); appended <- e }()
	select {
	case e = <-appended:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("daily packing blocked ingestion")
	}
	close(release)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	b := s.branches[Write]
	if len(b.summaryDays) != 1 || len(b.summaries) != 24 || len(b.accumulators) != 1 {
		t.Fatalf("state days=%d chunks=%d accumulators=%d", len(b.summaryDays), len(b.summaries), len(b.accumulators))
	}
	loc := b.summaryDays[start.UnixMilli()]
	raw, e := readBytes(b.sum, loc.Offset, loc.Offset+loc.Length)
	if e != nil {
		t.Fatal(e)
	}
	_, _, _, sums, e := decodeSummaryDay(raw)
	if e != nil || len(sums) != 24 {
		t.Fatal(e, len(sums))
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	cfg.sealHook = nil
	s, e = Open(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	b, e = s.openBranch(Write)
	if e != nil {
		t.Fatal(e)
	}
	if len(b.summaries) != 24 || len(b.accumulators) != 1 || len(b.summaryDays) != 1 {
		t.Fatal("recovery lost accumulators or packed day")
	}
	if dup, e := s.Append(Write, testFrame(t, l, now, 100, false)); e != nil || dup {
		t.Fatal("new receipt reused a discarded UUID identity", e)
	}
	for _, a := range b.accumulators {
		if a.columns.Count[0] != 2 {
			t.Fatal("new receipt missing from accumulator")
		}
	}
}

func TestDailySummaryAtomicPublicationFailures(t *testing.T) {
	for _, failure := range []string{"summary-day-ready", "summary-day-published"} {
		t.Run(failure, func(t *testing.T) {
			l := testLayout(t, false)
			start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
			cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return start.Add(48 * time.Hour) }, sealHook: func(stage string) error {
				if stage == failure {
					return errors.New("simulated interruption")
				}
				return nil
			}}
			s, e := Open(cfg)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.Append(Write, testFrame(t, l, start, 1, false)); e != nil {
				t.Fatal(e)
			}
			if e = s.Flush(); e == nil {
				t.Fatal("failure not injected")
			}
			s.Close()
			cfg.sealHook = nil
			s, e = Open(cfg)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			if e = s.Flush(); e != nil {
				t.Fatal(e)
			}
			b, e := s.openBranch(Write)
			if e != nil || len(b.summaries) != 1 || b.summaries[0].Columns.Count[0] != 1 {
				t.Fatal("lost committed summary", e)
			}
		})
	}
}

func TestDailySummaryHTTPFixture(t *testing.T) {
	l := testLayout(t, false)
	start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	now := start.Add(25 * time.Hour)
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	for _, branch := range []Branch{Read, Write, Error} {
		for i := 0; i < 288; i++ {
			if _, e = s.Append(branch, testFrame(t, l, start.Add(time.Duration(i)*5*time.Minute), uint32(i+1), i%3 == 0)); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	h := &HTTP{Store: s}
	get := func(path string, daily bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Accept", "application/pil")
		r.Header.Set("PIL-Version", "2")
		r.Header.Set("PIL-Features", "pac-arrays")
		if daily {
			r.Header.Set("PIL-Features", "pac-arrays,daily-summaries")
			r.Header.Set("Summary-Resolution", "month")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		return w
	}
	for _, border := range []bool{false, true} {
		from, to := start, start.Add(24*time.Hour)
		if border {
			from = from.Add(time.Minute)
			to = to.Add(-time.Minute)
		}
		path := "/events?" + url.Values{"tap": {"demo/payment/transactions.v1/*"}, "from": {from.Format(time.RFC3339)}, "to": {to.Format(time.RFC3339)}, "include": {"summary,statistics"}}.Encode()
		baseline := decodeView(t, l, get(path, false).Body.Bytes()).result
		dailyResponse := get(path, true).Body.Bytes()
		result := decodeView(t, l, dailyResponse).result
		if result.Analytics == nil || len(result.Analytics.Days) != 3 || len(result.Page) != 0 {
			t.Fatal("missing daily manifest")
		}
		if border {
			if len(result.Analytics.Rollups) != 0 {
				t.Fatal("unclipped rollup advertised for partial day")
			}
		} else {
			if len(result.Analytics.Rollups) != 3 {
				t.Fatal("missing monthly rollups")
			}
			for _, source := range result.Analytics.Rollups {
				if len(source.Buckets) != 1 || source.Buckets[0].Resolution != Monthly || source.Buckets[0].Count != 288 {
					t.Fatal("bad monthly rollup", source)
				}
			}
		}
		count := result.Summary.Count
		refs := make([]summaryPackQuery, len(result.Analytics.Days))
		for i, ref := range result.Analytics.Days {
			refs[i] = summaryPackQuery{Source: ref.Source, Definition: ref.Definition, Key: ref.Key}
		}
		response := requestSummaryPacks(t, h, refs)
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		var packets [][]byte
		if err := decodeSummaryTest(response.Body.Bytes(), &packets); err != nil {
			t.Fatal(err)
		}
		if len(packets) != len(refs) {
			t.Fatal("missing summary packs")
		}
		for i, raw := range packets {
			if summaryDigest(raw) != refs[i].Key {
				t.Fatal("pack order or identity changed")
			}
			_, _, _, sums, e := decodeSummaryDay(raw)
			if e != nil {
				t.Fatal(e)
			}
			for _, sum := range sums {
				for i, p := range sum.Columns.Period {
					if p >= result.Analytics.From && p+sumBucketMS <= result.Analytics.To {
						count += sum.Columns.Count[i]
					}
				}
			}
		}
		if count != baseline.Summary.Count {
			t.Fatalf("daily count %d != %d", count, baseline.Summary.Count)
		}
		if out := os.Getenv("TRUNK_SUMMARY_FIXTURE"); out != "" {
			dir := filepath.Join(out, map[bool]string{false: "aligned", true: "border"}[border])
			if e = os.MkdirAll(dir, 0755); e != nil {
				t.Fatal(e)
			}
			for i, raw := range packets {
				if e = os.WriteFile(filepath.Join(dir, result.Analytics.Days[i].Key+".sumday"), raw, 0644); e != nil {
					t.Fatal(e)
				}
			}
			if e = os.WriteFile(filepath.Join(dir, "view.pil"), dailyResponse, 0644); e != nil {
				t.Fatal(e)
			}
			metadata, _ := json.MarshalIndent(struct {
				Result   ViewResult   `json:"result"`
				Baseline *ViewSummary `json:"baseline"`
			}{result, baseline.Summary}, "", "  ")
			if e = os.WriteFile(filepath.Join(dir, "manifest.json"), metadata, 0644); e != nil {
				t.Fatal(e)
			}
		}
	}
}

func TestDailySummaryRetentionAndDirectDelivery(t *testing.T) {
	l := testLayout(t, false)
	start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	now := start.Add(48*time.Hour + 15*time.Minute)
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }}
	s, e := Open(cfg)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 48; i++ {
		if _, e = s.Append(Read, testFrame(t, l, start.Add(time.Duration(i)*time.Hour), uint32(i+1), false)); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	b := s.branches[Read]
	old := b.summaryDays[start.UnixMilli()]
	later := b.summaryDays[start.Add(24*time.Hour).UnixMilli()]
	oldPacket, e := readBytes(b.sum, later.Offset, later.Offset+later.Length)
	if e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	e = s.prune(Read, start.Add(12*time.Hour))
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	b = s.branches[Read]
	if len(b.summaries) != 36 {
		t.Fatal("retained summaries", len(b.summaries))
	}
	loc := b.summaryDays[start.UnixMilli()]
	raw, e := readBytes(b.sum, loc.Offset, loc.Offset+loc.Length)
	if e != nil {
		t.Fatal(e)
	}
	_, from, _, sums, e := decodeSummaryDay(raw)
	if e != nil || from != start.Add(12*time.Hour).UnixMilli() || len(sums) != 12 {
		t.Fatal("partial retained day", e, from, len(sums))
	}
	later = b.summaryDays[start.Add(24*time.Hour).UnixMilli()]
	raw, e = readBytes(b.sum, later.Offset, later.Offset+later.Length)
	if e != nil || !bytes.Equal(raw, oldPacket) {
		t.Fatal("unchanged day bytes", e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	b, e = s.openBranch(Read)
	if e != nil || len(b.summaries) != 36 {
		t.Fatal("retained day recovery", e)
	}
	h := &HTTP{Store: s}
	get := func(key string) *httptest.ResponseRecorder {
		return requestSummaryPacks(t, h, []summaryPackQuery{{Key: key, Source: h.tap(Read), Definition: fmtDigestNative(l.Digest)}})
	}
	if w := get(later.Key); w.Code != 200 {
		t.Fatal("direct disk delivery after restart", w.Code)
	} else {
		var packs [][]byte
		if err := decodeSummaryTest(w.Body.Bytes(), &packs); err != nil || len(packs) != 1 || !bytes.Equal(packs[0], oldPacket) {
			t.Fatal("changed sealed pack", err)
		}
	}
	if w := get(old.Key); w.Code != 409 {
		t.Fatal("retired packet remained accessible", w.Code)
	}
}

func requestSummaryPacks(t *testing.T, h *HTTP, refs []summaryPackQuery) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(struct {
		Packs []summaryPackQuery `json:"packs"`
	}{refs})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/summaries", bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decodeSummaryTest(raw []byte, out *[][]byte) error {
	l, err := pil.Compile(pil.Definition{Fields: []pil.Field{{Name: "packs", Type: "ARRAY<BYTES>"}}})
	if err != nil {
		return err
	}
	var f pil.Frame
	if err = l.Scan(raw, &f); err != nil {
		return err
	}
	items, err := pil.FieldValue(l.Definition.Fields[0], f.Values[0])
	if err != nil {
		return err
	}
	*out = make([][]byte, len(items.([]any)))
	for i, v := range items.([]any) {
		(*out)[i] = v.([]byte)
	}
	return nil
}
