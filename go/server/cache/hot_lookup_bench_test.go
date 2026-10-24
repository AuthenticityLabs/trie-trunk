package cache

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Full existing history API path, including lookup, snapshotting, PIL copies,
// and callback delivery. No network. Cold means expired resident frames; the OS
// page cache is deliberately warm. Sealing is completed outside the timed loop.
func BenchmarkResidentObjectHistory(b *testing.B) {
	for _, revisions := range []int{1, 9, 61} {
		b.Run(fmt.Sprint(revisions), func(b *testing.B) {
			l := testLayout(b, false)
			at := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
			now := at.Add(4 * time.Minute)
			p, err := OpenEndpoint(Config{Root: b.TempDir(), Cache: "demo", Tenant: "test", Layout: l, ObjectIndex: true, Now: func() time.Time { return now }})
			if err != nil {
				b.Fatal(err)
			}
			defer p.Close()
			var frames [][]byte
			for i := 0; i < revisions; i++ {
				frames = append(frames, testFrame(b, l, at.Add(time.Duration(i)*time.Millisecond), uint32(i+1), i%3 != 0))
			}
			if _, err = p.api.Store.AppendBatch(Write, frames); err != nil {
				b.Fatal(err)
			}
			for _, mode := range []string{"pending", "sealed-resident", "expired-resident-warm-disk"} {
				if mode == "sealed-resident" {
					now = at.Add(80 * time.Minute)
					if err = p.api.Store.Flush(); err != nil {
						b.Fatal(err)
					}
				}
				if mode == "expired-resident-warm-disk" {
					now = at.Add(26 * time.Hour)
				}
				// Confirm which path the benchmark actually exercises.
				h, e := p.snapshotHistory(l.Definition.Identity, 1, testObject(42), at, at.Add(time.Hour))
				if e != nil {
					b.Fatal(e)
				}
				if len(h.locations) != revisions {
					b.Fatalf("locations %d/%d", len(h.locations), revisions)
				}
				for _, v := range h.locations {
					if (v.row == ProvisionalRow) != (mode == "pending") {
						b.Fatal("wrong pending state")
					}
					if (v.raw != nil) != (mode != "expired-resident-warm-disk") {
						b.Fatal("wrong resident state")
					}
				}
				h.close()
				b.Run(mode, func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						count, size := 0, 0
						err := p.History(context.Background(), l.Definition.Identity, 1, testObject(42), at, at.Add(time.Hour), func(_ EventReference, raw []byte) error { count++; size += len(raw); return nil })
						if err != nil || count != revisions {
							b.Fatalf("history %d/%d: %v", count, revisions, err)
						}
						b.ReportMetric(float64(size), "PIL-B/op")
					}
				})
			}
		})
	}
}
