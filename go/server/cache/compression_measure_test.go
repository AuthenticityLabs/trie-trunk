package cache

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Explicit opt-in benchmark; fixture generation and correctness checks are
// outside timings. Same Go reader/HTTP path and durability for both variants.
func TestMeasureEventCompression(t *testing.T) {
	dir := os.Getenv("PIL_COMPRESSION_MEASURE")
	if dir == "" {
		t.Skip("set PIL_COMPRESSION_MEASURE")
	}
	if e := os.MkdirAll(dir, 0755); e != nil {
		t.Fatal(e)
	}
	type result struct {
		Profile, Mode                          string
		Count, Round                           int
		Raw, Wire, Population, Index, Response int
		EncodeNS, IngestNS, CollectNS          int64
		EncodeAlloc, IngestAlloc, CollectAlloc uint64
	}
	var results []result
	start := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	l := testLayout(t, false)
	d := l.Definition
	d.PIL = 1
	d.AdaptiveIntegers = "16"
	var err error
	l, err = Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"sparse", "text4k", "random4k"} {
		for _, count := range []int{50, 1000} {
			frames := make([][]byte, count)
			rawBytes := 0
			rng := rand.New(rand.NewSource(42))
			for n := range frames {
				var f Frame
				raw := compressionFrame(t, l, uint32(n+1), 4096)
				if e := l.Scan(raw, &f); e != nil {
					t.Fatal(e)
				}
				if profile == "sparse" {
					for i := 3; i < len(f.Values); i++ {
						f.Values[i] = Value{}
					}
					f.Values[4] = Uint(uint64(n%3), 1)
				}
				if profile == "random4k" {
					v := make([]byte, 4096)
					for i := range v {
						v[i] = byte(32 + rng.Intn(95))
					}
					f.Values[5] = Text(string(v))
				}
				raw, e := l.Encode(nil, f.Values)
				if e != nil {
					t.Fatal(e)
				}
				frames[n] = raw
				rawBytes += len(raw)
			}
			for round := 0; round < 3; round++ {
				for turn := 0; turn < 3; turn++ {
					level := []int{0, 1, 6}[(round+turn)%3]
					compressed := level != 0
					mode := "raw"
					if compressed {
						mode = fmt.Sprintf("gzip%d", level)
					}
					cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "bench", Layout: l, ObjectIndex: false, Now: func() time.Time { return start.Add(48 * time.Hour) }}
					store, e := Open(cfg)
					if e != nil {
						t.Fatal(e)
					}
					encoded := make([][]byte, count)
					var before, after runtime.MemStats
					runtime.ReadMemStats(&before)
					at := time.Now()
					for i, raw := range frames {
						encoded[i] = raw
						if compressed {
							encoded[i], e = l.CompressEventLevel(raw, level)
							if e != nil {
								t.Fatal(e)
							}
						}
					}
					encodeNS := time.Since(at).Nanoseconds()
					runtime.ReadMemStats(&after)
					encodeAlloc := after.TotalAlloc - before.TotalAlloc
					wire := bytes.Join(encoded, nil)
					api := &HTTP{Store: store}
					request := httptest.NewRequest("POST", "/ingest?tap=demo/payment/transactions.v1/write", bytes.NewReader(wire))
					request.Header.Set("Content-Type", "application/pil")
					request.Header.Set("PIL-Version", "2")
					request.Header.Set("Definition-Version", fmt.Sprintf("%x", l.Digest))
					request.Header.Set("PIL-Features", "adaptive-integers,"+CompressionFeature)
					reply := httptest.NewRecorder()
					runtime.ReadMemStats(&before)
					at = time.Now()
					api.ServeHTTP(reply, request)
					ingestNS := time.Since(at).Nanoseconds()
					runtime.ReadMemStats(&after)
					ingestAlloc := after.TotalAlloc - before.TotalAlloc
					if reply.Code != 200 {
						t.Fatal(reply.Code, reply.Body.String())
					}
					request = httptest.NewRequest("GET", "/collect?tap=demo/payment/transactions.v1/write&from=2026-10-18T00:00:00Z&to=2026-10-19T00:00:00Z", nil)
					request.Header.Set("Accept", "application/pil")
					request.Header.Set("PIL-Version", "2")
					request.Header.Set("PIL-Features", "adaptive-integers,"+CompressionFeature)
					reply = httptest.NewRecorder()
					runtime.ReadMemStats(&before)
					at = time.Now()
					benchmarkStandaloneCollect(reply, request, api)
					collectNS := time.Since(at).Nanoseconds()
					runtime.ReadMemStats(&after)
					collectAlloc := after.TotalAlloc - before.TotalAlloc
					if reply.Code != 200 || !bytes.HasSuffix(reply.Body.Bytes(), []byte{255, 254}) {
						t.Fatal("collect", reply.Code)
					}
					b := store.branches[Write]
					results = append(results, result{profile, mode, count, round, rawBytes, len(wire), int(b.popEnd) - headerSize, len(b.rows), reply.Body.Len(), encodeNS, ingestNS, collectNS, encodeAlloc, ingestAlloc, collectAlloc})
					if round == 0 {
						if e = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-%d-%s.pil", profile, count, mode)), reply.Body.Bytes(), 0600); e != nil {
							t.Fatal(e)
						}
					}
					if e = store.Close(); e != nil {
						t.Fatal(e)
					}
				}
			}
		}
	}
	data, _ := json.MarshalIndent(results, "", "  ")
	if e := os.WriteFile(filepath.Join(dir, "server.json"), data, 0644); e != nil {
		t.Fatal(e)
	}
}
