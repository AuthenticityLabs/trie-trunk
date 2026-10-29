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

func BenchmarkStateReplication(b *testing.B) {
	for _, count := range []int{1000, 50000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			s := &Store{state: &objectState{epoch: [16]byte{1}, sequence: uint64(count), objects: map[string]uint64{}}}
			for i := 0; i < count; i++ {
				s.state.objects[string(testObject(uint32(i)).Bytes)] = uint64(i + 1)
			}
			for _, mode := range []string{"legacy-full", "paged-full", "unchanged"} {
				b.Run(mode, func(b *testing.B) {
					have := Coverage{}
					if mode == "unchanged" {
						have.StateKnown = true
						have.StateEpoch = s.state.epoch
						have.StateSequence = s.state.sequence
					}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						snapshot, err := s.snapshotState(have, mode != "legacy-full")
						if err != nil {
							b.Fatal(err)
						}
						wire, maximum := 0, 0
						err = snapshot.send(func(_ uint64, raw []byte) error {
							wire += len(raw)
							if len(raw) > maximum {
								maximum = len(raw)
							}
							return nil
						})
						if err != nil {
							b.Fatal(err)
						}
						b.ReportMetric(float64(wire), "state-B/op")
						b.ReportMetric(float64(maximum), "max-chunk-B")
					}
				})
			}
		})
	}
}

func TestNativeStatePagesHTTP(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	s, err := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(4 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 1200; i++ {
		s.state.objects[string(testObject(uint32(i)).Bytes)] = uint64(i + 1)
	}
	s.state.sequence = 1200
	r := httptest.NewRequest("GET", "/collect?format=native&tap=demo/payment/transactions.v1/write&from=2026-10-16T12:00:00Z&to=2026-10-16T14:00:00Z", nil)
	r.Header.Set("Accept", "application/pil")
	r.Header.Set("Accept-Encoding", "gzip")
	r.Header.Set("PIL-Version", "2")
	r.Header.Set("PIL-Features", "native-ranges,native-state-pages,adaptive-integers")
	w := httptest.NewRecorder()
	(&HTTP{Store: s}).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	_, chunks := readNativeTest(t, w.Body.Bytes())
	pages, unchanged := 0, 0
	for _, chunk := range chunks {
		if chunk.Kind != "state" {
			continue
		}
		pages++
		var page statePage
		if err := decoder.Unmarshal(chunk.Data, &page); err != nil {
			t.Fatal(err)
		}
		if page.Unchanged {
			unchanged++
		}
	}
	if pages != 2 || unchanged != 0 {
		t.Fatal("merged delivery repeated state", pages, unchanged)
	}
	if dir := os.Getenv("PIL_STATE_FIXTURE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ranges.pil"), ungzipTest(t, w.Body.Bytes()), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNativeStatePagesBeyondLegacyArrayLimit(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	s, err := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(2 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const count = 65540
	for i := 0; i < count; i++ {
		s.state.objects[string(testObject(uint32(i)).Bytes)] = uint64(i + 1)
	}
	s.state.sequence = count
	request := CollectionRequest{Branch: Write, At: at, StatePages: true}
	seen, pages := 0, 0
	var previous []byte
	var manifest Manifest
	err = s.Collect(context.Background(), request, func(m Manifest) error { manifest = m; return nil }, func(chunk NativeChunk) error {
		if chunk.Kind != "state" {
			return nil
		}
		var page statePage
		if err := decoder.Unmarshal(chunk.Data, &page); err != nil {
			return err
		}
		if !page.Paged || page.Unchanged || page.Sequence != count || len(page.Objects) > 1024 || chunk.Start != uint64(seen) || len(chunk.Data) > 1<<20 {
			t.Fatal("invalid bounded page", len(chunk.Data), chunk.Start)
		}
		for _, entry := range page.Objects {
			if previous != nil && bytes.Compare(previous, entry.ID) >= 0 {
				t.Fatal("unordered snapshot")
			}
			previous = entry.ID
		}
		seen += len(page.Objects)
		pages++
		if page.Final != (seen == count) {
			t.Fatal("incorrect final boundary")
		}
		return nil
	})
	if err != nil || seen != count || pages != 65 {
		t.Fatal(seen, pages, err)
	}
	request.Have = Coverage{Definition: l.Digest, Generation: manifest.Generation, StateKnown: true, StateEpoch: manifest.StateEpoch, StateSequence: manifest.StateSequence}
	pages = 0
	err = s.Collect(context.Background(), request, func(Manifest) error { return nil }, func(chunk NativeChunk) error {
		if chunk.Kind != "state" {
			return nil
		}
		pages++
		var page statePage
		if err := decoder.Unmarshal(chunk.Data, &page); err != nil {
			return err
		}
		if !page.Unchanged || !page.Final || len(page.Objects) != 0 || chunk.Start != 0 {
			t.Fatal("unchanged state was duplicated")
		}
		return nil
	})
	if err != nil || pages != 1 {
		t.Fatal(pages, err)
	}
	request.StatePages = false
	if err = s.Collect(context.Background(), request, func(Manifest) error { return nil }, func(NativeChunk) error { return nil }); err == nil {
		t.Fatal("legacy oversized state accepted")
	}
}
