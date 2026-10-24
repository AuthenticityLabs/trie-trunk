package cache

// Opt-in, reproducible measurements. Normal go test runs only the small
// correctness tests; TRUNK_MEASURE_OUT enables the large fixture runs.
import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"testing"
	"time"
)

type timing struct {
	Samples []int64 `json:"samples_ns"`
	P50     int64   `json:"p50_ns"`
	P95     int64   `json:"p95_ns"`
}

func timed(t *testing.T, samples int, run func() error) timing {
	t.Helper()
	if e := run(); e != nil {
		t.Fatal(e)
	}
	v := timing{}
	for i := 0; i < samples; i++ {
		start := time.Now()
		if e := run(); e != nil {
			t.Fatal(e)
		}
		v.Samples = append(v.Samples, time.Since(start).Nanoseconds())
	}
	return timingSamples(v.Samples)
}

func timingSamples(samples []int64) timing {
	v := timing{Samples: samples}
	ordered := append([]int64(nil), v.Samples...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	v.P50, v.P95 = ordered[len(ordered)/2], ordered[(len(ordered)*95+99)/100-1]
	return v
}

func measurementSize(t *testing.T) int {
	t.Helper()
	if os.Getenv("TRUNK_MEASURE_OUT") == "" {
		t.Skip("opt-in storage measurement")
	}
	n, e := strconv.Atoi(os.Getenv("TRUNK_MEASURE_EVENTS"))
	if e != nil || n < 1 {
		t.Fatal("TRUNK_MEASURE_EVENTS must be positive")
	}
	return n
}
func saveMeasurement(t *testing.T, v any) {
	t.Helper()
	raw, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	path := os.Getenv("TRUNK_MEASURE_OUT")
	if e = os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, append(raw, '\n'), 0644); e != nil {
		t.Fatal(e)
	}
}

// Guard the base measurements against a policy/default accidentally enabling
// replay indexing. Check both the effective setting and the emitted files,
// outside the timed work. Identity columns in the primary index are separate.
func measurementWithoutObjectIndex(t *testing.T, s *Store) {
	t.Helper()
	if s.config.ObjectIndex || s.objects != nil {
		t.Fatal("base measurement must not enable the optional object index")
	}
	if _, err := os.Stat(filepath.Join(s.collection, "collection.idx")); !os.IsNotExist(err) {
		t.Fatalf("base measurement must not create collection.idx: %v", err)
	}
}

// Preserve the original fixture for historical aggregate measurements and
// correctness tests. Codec/native measurements explicitly choose a range-aware
// profile below; this wide-small-value fixture is not their default.
func measurementFixture(t testing.TB, n int, sparse bool) (*Layout, [][]byte, time.Time, time.Time, string) {
	return measurementFixtureProfile(t, n, sparse, "legacy-wide")
}

func measurementIntegerProfile(t testing.TB) string {
	t.Helper()
	profile := os.Getenv("TRUNK_MEASURE_INTEGERS")
	if profile == "" {
		profile = "range-sized"
	}
	switch profile {
	case "range-sized", "full-u64", "legacy-wide":
		return profile
	default:
		t.Fatalf("unknown TRUNK_MEASURE_INTEGERS %q", profile)
		return ""
	}
}

func smallestUnsigned(max uint64) string {
	switch {
	case max <= 0xff:
		return "U8"
	case max <= 0xffff:
		return "U16"
	case max <= 0xffffffff:
		return "U32"
	default:
		return "U64"
	}
}

// Fixed generator-domain bounds, not widths inferred per record. Revision's
// bound grows with the requested dataset size; all other domains stay fixed.
func measurementIntegerBounds(n int) map[int]uint64 {
	return map[int]uint64{1: 9999, 2: uint64((n-1)/10000 + 1), 3: 100049, 8: 99999, 9: 4999, 11: 3}
}

// Twelve fields, including strings, scalars, mapped values and identity roles.
// Sparse events carry the identity envelope and just one or two changed fields.
func measurementFixtureProfile(t testing.TB, n int, sparse bool, profile string) (*Layout, [][]byte, time.Time, time.Time, string) {
	t.Helper()
	if n <= 0 {
		t.Fatal("fixture requires positive event count")
	}
	d := Definition{Identity: "payment/transactions", Version: 1 << 20, Fields: []Field{
		{Name: "event_id", Type: "UUID"}, {Name: "id", Type: "U64"}, {Name: "revision_id", Type: "U64"},
		{Name: "duration", Type: "U64"}, {Name: "level", Type: "STRING", Values: []string{"INFO", "ERROR"}},
		{Name: "operation", Type: "STRING", Values: []string{"create", "update", "cancel"}},
		{Name: "state", Type: "STRING", Values: []string{"pending", "authorized", "canceled"}},
		{Name: "message", Type: "STRING"}, {Name: "amount", Type: "U64"}, {Name: "customer_id", Type: "U64"},
		{Name: "instance", Type: "STRING"}, {Name: "attempt", Type: "U32"},
	}, Resource: Binding{Format: Format, Service: "payment", Resource: "transactions", Major: 1, Timezone: "UTC",
		Roles: Roles{Event: "event_id", Object: "id", Revision: "revision_id", Duration: "duration"}, Path: []string{"level", "operation", "state"},
		Aggregations: []string{"count", "duration.count", "duration.sum", "duration.min", "duration.max"}}}
	switch profile {
	case "range-sized", "full-u64":
		// These are codec measurements with aggregates disabled. The optional
		// duration aggregation role currently requires U64; leave it unbound so
		// it cannot force an oversized codec field for this generator's range.
		d.Resource.Roles.Duration = ""
		d.Resource.Aggregations = nil
		for field, max := range measurementIntegerBounds(n) {
			d.Fields[field].Type = smallestUnsigned(max)
			if profile == "full-u64" {
				d.Fields[field].Type = "U64"
			}
		}
	case "legacy-wide":
	default:
		t.Fatalf("unknown fixture profile %q", profile)
	}
	l, e := Compile(d)
	if e != nil {
		t.Fatal(e)
	}
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 30)
	frames := make([][]byte, n)
	h := sha256.New()
	// Reproducible uniform uint64 samples, generated outside all timed work.
	// This is a type-range stress case, not a model of ordered revisions.
	rng := rand.New(rand.NewPCG(0x70696c, 0x63626f72))
	for i := range frames {
		ms := from.UnixMilli() + int64(i)*(to.UnixMilli()-from.UnixMilli())/int64(n)
		v := []Value{uuid(ms, uint32(i)), Uint(uint64(i%10000), l.Widths[1]), Uint(uint64(i/10000+1), l.Widths[2]), Uint(uint64(50+i*7919%100000), l.Widths[3]),
			Uint(uint64(i%20/19), 1), Uint(uint64(i%3), 1), Uint(uint64(i%3), 1), Text(fmt.Sprintf("Transaction %d completed", i)),
			Uint(uint64(i%100000), l.Widths[8]), Uint(uint64(i%5000), l.Widths[9]), Text(fmt.Sprintf("payment-%02d", i%12)), Uint(uint64(i%4), l.Widths[11])}
		if profile == "full-u64" {
			for _, field := range []int{1, 2, 3, 8, 9, 11} {
				v[field] = Uint(rng.Uint64(), 8)
			}
		}
		if sparse {
			amount := v[8]
			for j := 3; j < len(v); j++ {
				v[j] = Value{}
			}
			v[6] = Uint(uint64(i%3), 1)
			if i%2 == 0 {
				v[8] = amount
			}
		}
		frames[i], e = l.Encode(nil, v)
		if e != nil {
			t.Fatal(e)
		}
		h.Write(frames[i])
	}
	return l, frames, from, to, hex.EncodeToString(h.Sum(nil))
}

func TestMeasureAggregateStorage(t *testing.T) {
	n := measurementSize(t)
	l, frames, from, to, hash := measurementFixture(t, n, false)
	config := Config{Root: t.TempDir(), Cache: "bench", Tenant: "measure", Layout: l, ObjectIndex: false, Now: func() time.Time { return to.Add(time.Hour) }}
	s, e := Open(config)
	if e != nil {
		t.Fatal(e)
	}
	start := time.Now()
	for _, f := range frames {
		if _, e = s.Append(Write, f); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	ingest := time.Since(start).Nanoseconds()
	var reopen timing
	reopen = timed(t, 9, func() error {
		var err error
		s, err = Open(config)
		if err != nil {
			return err
		}
		return s.Close()
	})
	s, e = Open(config)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	b := s.branches[Write]
	stat, e := b.sum.Stat()
	if e != nil {
		t.Fatal(e)
	}
	var rawRecords [][]byte
	if e = boundedRecords(b.sum, maxSummaryDay+32, func(raw []byte) error { rawRecords = append(rawRecords, raw); return nil }); e != nil {
		t.Fatal(e)
	}
	var groupCount, eventCount uint64
	for _, summary := range b.summaries {
		for i := 0; i < summary.Columns.Len(); i++ {
			g := summary.Columns.At(i)
			groupCount++
			eventCount += g.Aggregate.Count
		}
	}
	if eventCount != uint64(n) {
		t.Fatal("aggregate event count mismatch")
	}
	encode := timed(t, 9, func() error {
		for _, v := range b.summaries {
			if _, e := encodeSummary(v); e != nil {
				return e
			}
		}
		return nil
	})
	decode := timed(t, 9, func() error {
		for _, raw := range rawRecords {
			if len(raw) >= 8 && string(raw[:8]) == "SUMDAY00" {
				_, _, _, _, e := decodeSummaryDay(raw)
				if e != nil {
					return e
				}
				continue
			}
			var v Summary
			if e := decodeSummary(raw, &v); e != nil {
				return e
			}
		}
		return nil
	})
	read := timed(t, 9, func() error {
		return boundedRecords(b.sum, maxSummaryDay+32, func(raw []byte) error {
			if len(raw) >= 8 && string(raw[:8]) == "SUMDAY00" {
				_, _, _, _, e := decodeSummaryDay(raw)
				return e
			}
			var v Summary
			return decodeSummary(raw, &v)
		})
	})
	queries := map[string]timing{}
	for _, q := range []struct {
		name     string
		from, to time.Time
	}{{"all", from, to}, {"day", from.AddDate(0, 0, 15), from.AddDate(0, 0, 16)}, {"three_hours", from.AddDate(0, 0, 15).Add(9 * time.Hour), from.AddDate(0, 0, 15).Add(12 * time.Hour)}} {
		queries[q.name] = timed(t, 9, func() error { _, e := s.Summaries(Write, q.from, q.to); return e })
	}
	for day := from; day.Before(to); day = day.AddDate(0, 0, 1) {
		if e = s.PublishRollup(Write, Daily, day); e != nil {
			t.Fatal(e)
		}
	}
	rollupPath, _, _, e := s.rollupPeriod(Write, Daily, from)
	if e != nil {
		t.Fatal(e)
	}
	rst, e := os.Stat(rollupPath)
	if e != nil {
		t.Fatal(e)
	}
	rollupRead := timed(t, 9, func() error {
		var count uint64
		for day := from; day.Before(to); day = day.AddDate(0, 0, 1) {
			g, e := s.ReadRollup(Write, Daily, day)
			if e != nil {
				return e
			}
			for _, a := range g {
				count += a.Count
			}
		}
		if count != uint64(n) {
			return fmt.Errorf("rollup count mismatch")
		}
		return nil
	})
	measurementWithoutObjectIndex(t, s)
	saveMeasurement(t, map[string]any{"events": n, "object_index": false, "object_index_bytes": 0, "row_width": s.row.Width, "fixture_sha256": hash, "go": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH, "aggregate_rows": groupCount, "aggregate_events": eventCount, "sum_bytes": stat.Size(), "rollup_bytes": rst.Size(), "ingest_ns": ingest, "reopen": reopen, "encode": encode, "decode": decode, "file_read_decode": read, "query": queries, "rollup_read_30_days": rollupRead})
}
