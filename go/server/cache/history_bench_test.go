package cache

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Both paths use the same store, optional object postings, cached map and PIL
// population reader. Snapshot cost buys unlocked network callbacks; no IPC or
// encoding format change is included in this comparison.
func BenchmarkHistorySnapshot(b *testing.B) {
	for _, count := range []int{10, 1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			l := testLayout(b, false)
			at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
			p, err := OpenEndpoint(Config{Root: b.TempDir(), Cache: "demo", Tenant: "test", Layout: l, ObjectIndex: true, Now: func() time.Time { return at.Add(2 * time.Hour) }})
			if err != nil {
				b.Fatal(err)
			}
			defer p.Close()
			s := p.api.Store
			frames := make([][]byte, count)
			for i := range frames {
				frames[i] = testFrame(b, l, at.Add(time.Duration(i)*time.Millisecond), uint32(i+1), i%3 != 0)
			}
			if _, err = s.AppendBatch(Write, frames); err != nil {
				b.Fatal(err)
			}
			// Warm deferred object-posting recovery and filesystem pages before
			// either timed reader; cold-start work is not a codec comparison.
			if err = s.History(context.Background(), testObject(42), at, at.Add(time.Hour), func(uint64, []byte) error { return nil }); err != nil {
				b.Fatal(err)
			}
			for _, mode := range []string{"locked-reader", "snapshot-reader"} {
				b.Run(mode, func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						seen, size := 0, 0
						visit := func(raw []byte) { seen++; size += len(raw) }
						if mode == "locked-reader" {
							err = s.History(context.Background(), testObject(42), at, at.Add(time.Hour), func(_ uint64, raw []byte) error { visit(raw); return nil })
						} else {
							err = p.History(context.Background(), l.Definition.Identity, 1, testObject(42), at, at.Add(time.Hour), func(_ EventReference, raw []byte) error { visit(raw); return nil })
						}
						if err != nil || seen != count {
							b.Fatalf("%d/%d: %v", seen, count, err)
						}
						b.ReportMetric(float64(size), "PIL-B/op")
					}
				})
			}
		})
	}
}
