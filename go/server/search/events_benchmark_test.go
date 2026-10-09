package search

import (
	"context"
	"io"
	"os"
	"strconv"
	"testing"
	"time"
	"trie-trunk/go/codec"
	"trie-trunk/go/server/store"
)

// Use a release-built trunk-search on PATH. Fixture construction is outside timing.
func BenchmarkEventsResident(b *testing.B) {
	count := 100000
	if n, e := strconv.Atoi(os.Getenv("TRUNK_BENCH_EVENTS")); e == nil && n > 0 {
		count = n
	}
	now := time.Now().UTC()
	start := now.Truncate(time.Hour).Add(-2 * time.Hour)
	c := store.Config{Root: b.TempDir(), Tap: "bench", MaxPartitions: 32}
	m, e := store.Open(c)
	if e != nil {
		b.Fatal(e)
	}
	d := testDefinition()
	d.Storage = &codec.StoragePolicy{Format: 3, Boundary: "http_status", Bucket: "hour"}
	rev, data, e := d.Prepare()
	if e != nil {
		b.Fatal(e)
	}
	if e = m.Install(rev, data); e != nil {
		b.Fatal(e)
	}
	for i := 0; i < count; i++ {
		id, e := codec.NewUUIDv7(start.Add(time.Duration(i) * time.Millisecond))
		if e != nil {
			b.Fatal(e)
		}
		raw, e := codec.MarshalCompact(codec.Record{8: id, 12: uint64(i % 50000), 3: uint64(5), 14: uint64(400 + i%8), 19: "/secrets"}, codec.CBOR)
		if e != nil {
			b.Fatal(e)
		}
		if _, e = m.Append(rev, raw); e != nil {
			b.Fatal(e)
		}
	}
	if e = m.Close(); e != nil {
		b.Fatal(e)
	}
	q := Config{Root: c.Root, Start: start, End: start.Add(time.Duration(count) * time.Millisecond), PIL: true, PILV2: true}
	for _, workers := range []string{"1", "4"} {
		b.Run("workers-"+workers, func(b *testing.B) {
			b.Setenv("TRUNK_SEARCH_WORKERS", workers)
			engine := NewEngine(c.Root, 1)
			defer engine.Close()
			if e = engine.Run(context.Background(), q, io.Discard); e != nil {
				b.Fatal(e)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if e = engine.Run(context.Background(), q, io.Discard); e != nil {
					b.Fatal(e)
				}
			}
			b.ReportMetric(float64(count)*float64(b.N)/b.Elapsed().Seconds(), "events/s")
		})
	}
}
