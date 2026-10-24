package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Same fixture and public API before/after. Run in isolated stores with the
// optional replay index and aggregates disabled. Setup and correctness hashes
// are outside query timing; native collection never requests standalone frames.
func TestMeasureNativeStorage(t *testing.T) {
	n := measurementSize(t)
	sparse := os.Getenv("TRUNK_MEASURE_SHAPE") == "sparse"
	profile := measurementIntegerProfile(t)
	l, frames, from, to, hash := measurementFixtureProfile(t, n, sparse, profile)
	d := l.Definition
	d.Resource.Aggregations = nil
	l, err := Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	c := Config{Root: t.TempDir(), Cache: "bench", Tenant: "measure", Layout: l, ObjectIndex: false, Now: func() time.Time { return to.Add(time.Hour) }}
	s, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for _, frame := range frames {
		if duplicate, err := s.Append(Write, frame); err != nil || duplicate {
			t.Fatalf("append: %v %v", duplicate, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ingest := time.Since(start).Nanoseconds()
	start = time.Now()
	s, err = Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reopen := time.Since(start).Nanoseconds()
	measurementWithoutObjectIndex(t, s)
	sizes := map[string]int64{}
	files := 0
	if err := filepath.WalkDir(s.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		sizes[filepath.Ext(path)] += info.Size()
		files++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	queries := map[string]timing{}
	counts := map[string]int{}
	hashes := map[string]string{}
	native := map[string]timing{}
	bytes := map[string]map[string]int64{}
	for _, q := range []struct {
		name     string
		from, to time.Time
	}{
		{"all", from, to},
		{"day", from.AddDate(0, 0, 15), from.AddDate(0, 0, 16)},
		{"three_hours", from.AddDate(0, 0, 15).Add(9 * time.Hour), from.AddDate(0, 0, 15).Add(12 * time.Hour)},
	} {
		query := Query{Branch: Write, From: q.from, To: q.to}
		digest := sha256.New()
		if err := s.Search(context.Background(), query, func(_ uint64, raw []byte) error { counts[q.name]++; digest.Write(raw); return nil }); err != nil {
			t.Fatal(err)
		}
		hashes[q.name] = hex.EncodeToString(digest.Sum(nil))
		if q.name == "all" && (counts[q.name] != n || hashes[q.name] != hash) {
			t.Fatal("standalone result differs from source")
		}
		queries[q.name] = timed(t, 9, func() error { return s.Search(context.Background(), query, func(uint64, []byte) error { return nil }) })
		if q.name == "all" {
			continue
		}
		request := CollectionRequest{Branch: Write, Day: q.from, PopulationFrom: q.from, PopulationTo: q.to}
		var m Manifest
		for _, cached := range []bool{false, true} {
			key := q.name + "_initial"
			if cached {
				key = q.name + "_cached"
			}
			bytes[key] = map[string]int64{}
			if err := s.Collect(context.Background(), request, func(v Manifest) error { m = v; return nil }, func(c NativeChunk) error {
				if sha256.Sum256(c.Data) != c.Digest {
					t.Fatal("chunk checksum mismatch")
				}
				bytes[key][c.Kind] += int64(len(c.Data))
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if cached && (bytes[key]["index"] != 0 || bytes[key]["population"] != 0) {
				t.Fatal("duplicate native transfer")
			}
			native[key] = timed(t, 9, func() error {
				return s.Collect(context.Background(), request, func(Manifest) error { return nil }, func(NativeChunk) error { return nil })
			})
			request.Have = Coverage{Definition: m.Definition, Generation: m.Generation, Index: m.Index, Population: m.Population, TrieNodes: m.TrieNodes}
		}
	}
	saveMeasurement(t, map[string]any{"events": n, "sparse": sparse, "format": Format, "fixture_sha256": hash, "go": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH,
		"integer_profile": profile, "definition": l.Definition,
		"object_index": false, "object_index_bytes": 0, "aggregates": false, "row_width": s.row.Width, "logical_bytes": sizes, "file_count": files, "ingest_ns": ingest, "reopen_ns": reopen,
		"standalone_query": queries, "result_counts": counts, "result_hashes": hashes, "native_collection": native, "native_bytes": bytes})
}
