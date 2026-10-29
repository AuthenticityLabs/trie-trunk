package cache

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Opt-in reproducible workload fixture for real browser codec/OPFS benchmarks.
func TestFormatComparisonFixture(t *testing.T) {
	out := os.Getenv("TRUNK_FORMAT_BENCH_OUTPUT")
	if out == "" {
		t.Skip("set TRUNK_FORMAT_BENCH_OUTPUT to generate the million-event fixture")
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
	started := time.Now()
	digest := sha256.New()
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
			var seed [8]byte
			binary.LittleEndian.PutUint64(seed[:], uint64(ordinal))
			entropy := sha256.Sum256(seed[:])
			copy(id[6:], entropy[:10])
			id[6] = (id[6] & 15) | 0x70
			id[8] = (id[8] & 63) | 0x80
			var canonical Frame
			if err = l.Scan(raw, &canonical); err != nil {
				t.Fatal(err)
			}
			canonical.Values[l.Event] = Value{State: Direct, Bytes: id[:]}
			raw, err = l.Encode(nil, canonical.Values)
			if err != nil {
				t.Fatal(err)
			}
			digest.Write(id[:])
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

		if hour == 0 {
			run := b.runs[len(b.runs)-1]
			stored, e := readBytes(b.pods, run.PodsStart-b.podsRetired, run.PodsEnd-b.podsRetired)
			if e != nil {
				t.Fatal(e)
			}
			original, e := decodeStoredPAC(l, run, stored)
			if e != nil {
				t.Fatal(e)
			}
			variants := map[string]any{}
			for _, omitRecord := range []bool{false, true} {
				for _, omitDirectory := range []bool{false, true} {
					q := *original
					q.Directory = nil
					q.Members = append([][]byte(nil), original.Members...)
					if omitRecord {
						for i, raw := range q.Members {
							var f Frame
							if e = l.Scan(raw, &f); e != nil {
								t.Fatal(e)
							}
							f.Values[l.Record] = Value{}
							q.Members[i], e = l.Layout.Encode(nil, f.Values)
							if e != nil {
								t.Fatal(e)
							}
						}
					}
					if omitDirectory {
						q.AddressBytes = 0
					}
					decoded, e := q.Marshal()
					if e != nil {
						t.Fatal(e)
					}
					compressed, e := compressPAC(decoded, s.policyFor(b.branch).PAC.level())
					if e != nil {
						t.Fatal(e)
					}
					name := fmt.Sprintf("record-%t-directory-%t", !omitRecord, !omitDirectory)
					if e = os.MkdirAll(filepath.Dir(out), 0755); e != nil {
						t.Fatal(e)
					}
					if e = os.WriteFile(filepath.Join(filepath.Dir(out), name+".pac"), compressed, 0644); e != nil {
						t.Fatal(e)
					}
					variants[name] = map[string]any{"decoded_bytes": len(decoded), "compressed_bytes": len(compressed), "events": len(q.Members)}
				}
			}
			raw, _ := json.MarshalIndent(variants, "", "  ")
			if e = os.WriteFile(filepath.Join(filepath.Dir(out), "variants.json"), raw, 0644); e != nil {
				t.Fatal(e)
			}
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
	sealMs := float64(time.Since(started).Nanoseconds()) / 1e6
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	started = time.Now()
	s, err = Open(s.config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reopenMs := float64(time.Since(started).Nanoseconds()) / 1e6
	started = time.Now()
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
	var manifest map[string]any
	if err = json.Unmarshal([]byte(meta), &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["input_uuid_sha256"] = fmt.Sprintf("%x", digest.Sum(nil))
	manifest["index_row_bytes"] = IndexRowSize
	manifest["native_memory"] = s.NativeMemory()
	manifest["seal_ms"] = sealMs
	manifest["reopen_ms"] = reopenMs
	sizes := map[string]int64{}
	var logical, allocated int64
	var files int
	err = filepath.WalkDir(s.config.Root, func(path string, entry fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if entry.IsDir() {
			return nil
		}
		info, e := entry.Info()
		if e != nil {
			return e
		}
		sizes[filepath.Ext(path)] += info.Size()
		logical += info.Size()
		allocated += info.Sys().(*syscall.Stat_t).Blocks * 512
		files++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest["allocated_bytes"] = allocated
	manifest["logical_bytes"] = sizes
	manifest["total_logical_bytes"] = logical
	manifest["files"] = files
	manifest["scope"] = "Entire isolated resource store; summaries enabled; Tantivy and collection index disabled; same deterministic input events; ingress entropy discarded; sealed U24 ordinal"
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(out+".json", encoded, 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d hours, %d events, %d wire bytes, %d total stored bytes", hours, hours*perHour, w.Body.Len(), logical)
}
