package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Opt-in end-to-end storage/handler measurement. The two delivery paths use one
// immutable dataset and the same Go handler/sink, with no Rust IPC difference.
func TestMeasureIntegratedCollection(t *testing.T) {
	n := measurementSize(t)
	source, frames, from, to, _ := measurementFixtureProfile(t, n, false, "range-sized")
	d := source.Definition
	d.PIL = 1
	d.AdaptiveIntegers = "16"
	l, err := Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	var frame Frame
	dense, sparse := 0, 0
	for i, raw := range frames {
		if err = source.Scan(raw, &frame); err != nil {
			t.Fatal(err)
		}
		if i%4 != 0 {
			state, amount := frame.Values[6], frame.Values[8]
			for field := 3; field < len(frame.Values); field++ {
				frame.Values[field] = Value{}
			}
			frame.Values[6] = state
			if i%2 == 0 {
				frame.Values[8] = amount
			}
			sparse++
		} else {
			dense++
		}
		frames[i], err = l.Encode(nil, frame.Values)
		if err != nil {
			t.Fatal(err)
		}
		h.Write(frames[i])
	}
	expected := hex.EncodeToString(h.Sum(nil))
	replay := os.Getenv("TRUNK_MEASURE_OBJECTS") == "true"
	cfg := Config{Root: t.TempDir(), Cache: "bench", Tenant: "measure", Layout: l, ObjectIndex: replay, Now: func() time.Time { return to.Add(time.Hour) }}
	s, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for at := 0; at < n; at += 1024 {
		end := min(at+1024, n)
		if count, e := s.AppendBatch(Write, frames[at:end]); e != nil || count != end-at {
			t.Fatal(count, e)
		}
	}
	acknowledged := time.Since(start).Nanoseconds()
	start = time.Now()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	finalize := time.Since(start).Nanoseconds()
	frames = nil
	runtime.GC()
	start = time.Now()
	s, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	reopen := time.Since(start).Nanoseconds()
	defer s.Close()
	if !replay {
		measurementWithoutObjectIndex(t, s)
	}
	h.Reset()
	seen := 0
	if err = s.Search(context.Background(), Query{Branch: Write, From: from, To: to}, func(_ uint64, raw []byte) error { seen++; h.Write(raw); return nil }); err != nil {
		t.Fatal(err)
	}
	if seen != n || hex.EncodeToString(h.Sum(nil)) != expected {
		t.Fatal("stored events differ from encoded input")
	}
	sizes := map[string]int64{}
	files := 0
	if err = filepath.WalkDir(s.root, func(path string, entry os.DirEntry, e error) error {
		if e != nil || entry.IsDir() {
			return e
		}
		info, e := entry.Info()
		if e != nil {
			return e
		}
		sizes[filepath.Base(path)] += info.Size()
		files++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b := s.branches[Write]
	known := Coverage{Definition: l.Digest, Generation: b.generation, Index: []Span{{0, uint64(n)}}, Population: []Span{{headerSize, b.popEnd}}, TrieNodes: len(s.trie.nodes), StateKnown: true, StateEpoch: s.state.epoch, StateSequence: s.state.sequence}
	coverage, _ := json.Marshal([]Coverage{known})
	handler := &HTTP{Store: s}
	results := map[string]any{}
	for _, window := range []struct {
		name     string
		from, to time.Time
	}{{"all", from, to}, {"day", from.AddDate(0, 0, 15), from.AddDate(0, 0, 16)}, {"three_hours", from.AddDate(0, 0, 15).Add(9 * time.Hour), from.AddDate(0, 0, 15).Add(12 * time.Hour)}} {
		queryCount := 0
		if err = s.Search(context.Background(), Query{Branch: Write, From: window.from, To: window.to}, func(uint64, []byte) error { queryCount++; return nil }); err != nil {
			t.Fatal(err)
		}
		for _, mode := range []string{"standalone", "native-cold", "native-index-only", "native-warm"} {
			q := url.Values{"tap": {handler.tap(Write)}, "from": {window.from.Format(time.RFC3339Nano)}, "to": {window.to.Format(time.RFC3339Nano)}}
			if mode != "standalone" {
				q.Set("format", "native")
			}
			if mode == "native-index-only" {
				q.Set("population", "none")
			}
			req := httptest.NewRequest("GET", "/collect?"+q.Encode(), nil)
			req.Header.Set("Accept", "application/pil")
			req.Header.Set("Accept-Encoding", "gzip")
			req.Header.Set("PIL-Version", "2")
			req.Header.Set("PIL-Features", "native-ranges,native-state-pages,adaptive-integers")
			req.Header.Set("Definition-Known", fmtDigestNative(l.Digest))
			if mode == "native-warm" {
				req.Header.Set("Trunk-Coverage", base64.RawURLEncoding.EncodeToString(coverage))
			}
			// Correctness parse and byte comparison are outside measured handler work.
			response := httptest.NewRecorder()
			if mode == "standalone" {
				benchmarkStandaloneCollect(response, req, handler)
			} else {
				handler.ServeHTTP(response, req)
			}
			if response.Code != 200 || response.Header().Get("Trunk-Status") != "ok" {
				t.Fatal(mode, response.Code, response.Header())
			}
			payload := map[string]int{}
			if mode == "standalone" {
				count := verifyMeasuredPIL(t, l, response.Body.Bytes())
				if count != queryCount {
					t.Fatal(count, queryCount)
				}
			} else {
				manifests, chunks := readNativeTest(t, response.Body.Bytes())
				windows, e := s.PlanPackages(window.from, window.to)
				if e != nil || len(manifests) != len(windows) {
					t.Fatal("package count", len(manifests), len(windows), e)
				}
				for _, m := range manifests {
					if m.Provisional {
						t.Fatal("mature fixture package is provisional")
					}
				}
				for _, chunk := range chunks {
					payload[chunk.Kind] += len(chunk.Data)
					switch chunk.Kind {
					case "index":
						if !bytes.Equal(chunk.Data, b.rows[chunk.Start*IndexRowSize:chunk.Start*IndexRowSize+uint64(len(chunk.Data))]) {
							t.Fatal("index bytes changed")
						}
					case "population":
						want := make([]byte, len(chunk.Data))
						if _, e := b.pop.ReadAt(want, int64(chunk.Start)); e != nil || !bytes.Equal(chunk.Data, want) {
							t.Fatal("population bytes changed", e)
						}
					}
				}
				if mode != "native-warm" && payload["index"] != queryCount*IndexRowSize {
					t.Fatal("incomplete hourly index delivery", payload["index"], queryCount)
				}
				if mode == "native-cold" && window.name == "all" && uint64(payload["population"]) != b.popEnd-headerSize {
					t.Fatal("incomplete full population delivery")
				}
				if mode == "native-warm" && (payload["index"] != 0 || payload["population"] != 0 || payload["trie"] != 0) {
					t.Fatal("cached bytes retransmitted")
				}
				if mode == "native-index-only" && payload["population"] != 0 {
					t.Fatal("eager population in lazy request")
				}
			}
			size := response.Body.Len()
			response = nil
			run := func() error {
				w := &countedResponse{header: make(http.Header)}
				if mode == "standalone" {
					benchmarkStandaloneCollect(w, req, handler)
				} else {
					handler.ServeHTTP(w, req)
				}
				if w.bytes != size || w.header.Get("Trunk-Status") != "ok" {
					return fmt.Errorf("inconsistent delivery")
				}
				return nil
			}
			times := timed(t, 9, run)
			// Allocation accounting is separate from latency samples. Runtime counters
			// include all Go allocations during one query (including GC bookkeeping).
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			if err = run(); err != nil {
				t.Fatal(err)
			}
			runtime.ReadMemStats(&after)
			results[window.name+"/"+mode] = map[string]any{"latency": times, "wire_bytes": size, "payload_bytes": payload, "events": queryCount, "allocated_bytes": after.TotalAlloc - before.TotalAlloc, "allocations": after.Mallocs - before.Mallocs}
		}
	}
	saveMeasurement(t, map[string]any{"events": n, "dense_events": dense, "sparse_events": sparse, "definition": l.Definition, "fixture_sha256": expected, "row_width": IndexRowSize, "object_index": replay, "aggregates": false, "file_bytes": sizes, "file_count": files, "acknowledged_ingest_ns": acknowledged, "finalization_ns": finalize, "reopen_ns": reopen, "batch_records": 1024, "queries": results, "go": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH, "scope": "temporary filesystem, durable batched ingestion then finalization, warm OS page-cache handler requests; no socket/TLS/WAN, compression or browser rendering; maps cached in both delivery modes"})
}
func verifyMeasuredPIL(t *testing.T, l *Layout, raw []byte) int {
	t.Helper()
	if !bytes.HasPrefix(raw, []byte{'T', 'P', 'I', 'L', 2}) {
		t.Fatal("invalid PIL preamble")
	}
	r := bytes.NewReader(raw[5:])
	count := 0
	var frame []byte
	for {
		tag, e := r.ReadByte()
		if e != nil {
			t.Fatal(e)
		}
		switch tag {
		case 2:
			if _, e = nativeControl("U32", "BYTES", "STRING").ReadFrame(r, nil); e != nil {
				t.Fatal(e)
			}
		case 3:
			var stream [4]byte
			if _, e = io.ReadFull(r, stream[:]); e != nil {
				t.Fatal(e)
			}
			frame, e = l.ReadFrame(r, frame)
			if e != nil {
				t.Fatal(e)
			}
			count++
		case 255:
			last, e := r.ReadByte()
			if e != nil || last != 254 || r.Len() != 0 {
				t.Fatal("incomplete PIL")
			}
			return count
		default:
			t.Fatal("unexpected PIL tag", tag)
		}
	}
}
