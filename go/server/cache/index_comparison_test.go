package cache

import (
	"fmt"
	"testing"
)

var comparisonOrdinal uint64

func BenchmarkFormatIndex(b *testing.B) {
	const base = int64(1792584000000)
	for _, n := range []int{1000000, 16000000} {
		source := branchStore{rows: make([]byte, n*IndexRowSize)}
		source.runs = []Run{{Start: 0, End: uint64(n), MinMS: base, MaxMS: base + int64((n-1)/5), PAC: &StoredPAC{Placement: PACPlacement{From: base}}}}
		for i := 0; i < n; i++ {
			if e := packIndexID(source.rows[i*IndexRowSize:], [16]byte(uuid(base+int64(i/5), uint32(i)).Bytes), base); e != nil {
				b.Fatal(e)
			}
		}
		b.Run(fmt.Sprint(n)+"/lower", func(b *testing.B) {
			b.ReportAllocs()
			b.ReportMetric(float64(len(source.rows)), "index_bytes")
			var seed uint32 = 13
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				seed = seed*1664525 + 1013904223
				comparisonOrdinal = source.lower(0, uint64(n), base+int64(seed%uint32(n/5)))
			}
		})
	}
}
