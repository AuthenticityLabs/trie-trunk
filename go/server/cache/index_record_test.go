package cache

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"sort"
	"testing"
)

func TestIndexUUIDRoundTripAndOrdering(t *testing.T) {
	base := int64(1792584000000)
	rng := rand.New(rand.NewPCG(42, 7))
	var previous [16]byte
	for i := 0; i < 100000; i++ {
		delta := int64(i * 35)
		v := uuid(base+delta, uint32(rng.Uint64()))
		id := [16]byte(v.Bytes)
		id[6] = 0x70 | byte(rng.Uint64()&15)
		id[7] = byte(rng.Uint64())
		id[8] = 0x80 | byte(rng.Uint64()&63)
		for j := 9; j < 16; j++ {
			id[j] = byte(rng.Uint64())
		}
		var row [IndexRowSize]byte
		if e := packIndexID(row[:], id, base); e != nil {
			t.Fatal(e)
		}
		if got := unpackIndexID(row[:], base); got != id {
			t.Fatalf("lost UUID bits %x != %x", got, id)
		}
		if indexDelta(row[:]) != delta {
			t.Fatal("timestamp delta")
		}
		if i > 0 {
			var before [IndexRowSize]byte
			_ = packIndexID(before[:], previous, base)
			if bytes.Compare(before[:12], row[:12]) >= 0 {
				t.Fatal("order changed")
			}
		}
		previous = id
	}
	for _, delta := range []int64{-1, 1 << 22} {
		id := [16]byte(uuid(base+delta, 1).Bytes)
		if packIndexID(make([]byte, 18), id, base) == nil {
			t.Fatal("invalid delta accepted")
		}
	}
}

var benchOrdinal int
var benchUUID [16]byte
var benchLength uint32

// Same rows and lookup sequence; compare the prior memberLength implementation
// with the overlapping window. No storage I/O or PAC decoding is included.
func BenchmarkIndexMemberWindow(b *testing.B) {
	const segments, records = 1024, 1024
	source := branchStore{rows: make([]byte, segments*(records+1)*IndexRowSize)}
	for i := 0; i < segments; i++ {
		start := uint64(i*(records+1) + 1)
		source.runs = append(source.runs, Run{Start: start, End: start + records, PAC: &StoredPAC{Placement: PACPlacement{MemberBytes: records * 37}}})
		for j := 0; j < records; j++ {
			order.PutUint32(source.row(start + uint64(j))[12:], uint32(j)*37)
		}
	}
	for _, scattered := range []bool{false, true} {
		pattern := "sequential"
		if scattered {
			pattern = "scattered"
		}
		for _, window := range []bool{false, true} {
			mode := "explicit_next_start"
			if window {
				mode = "current_starts"
			}
			b.Run(pattern+"/"+mode, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					n := i % (segments * records)
					if scattered {
						n = (i * 7919) % (segments * records)
					}
					row := uint64(n/records*(records+1) + 1 + n%records)
					if window {
						benchLength = source.memberLength(row)
					} else {
						start := order.Uint32(source.row(row)[12:])
						run := source.runs[source.runIndex(row)]
						end := uint32(run.PAC.Placement.MemberBytes)
						if row+1 < run.End {
							end = order.Uint32(source.row(row + 1)[12:])
						}
						benchLength = end - start
					}
				}
			})
		}
	}
}

func BenchmarkIndexHour(b *testing.B) {
	const n = 1000000
	const base = int64(1792584000000)
	compact := make([]byte, n*IndexRowSize)
	for i := 0; i < n; i++ {
		id := [16]byte(uuid(base+int64(i)*3, uint32(i)).Bytes)
		_ = packIndexID(compact[i*IndexRowSize:], id, base)
	}
	for _, stride := range []int{18, 16} {
		data := make([]byte, n*stride)
		for i := 0; i < n; i++ {
			copy(data[i*stride:], compact[i*IndexRowSize:(i+1)*IndexRowSize])
		}
		b.Run(fmt.Sprintf("hour%d", stride), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			b.ReportMetric(float64(len(data)), "index_bytes")
			for i := 0; i < b.N; i++ {
				target := int64((i*7919)%n) * 3
				benchOrdinal = sort.Search(n, func(k int) bool { return indexDelta(data[k*stride:]) >= target })
			}
		})
	}
	b.Run("reconstruct_uuid", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			benchUUID = unpackIndexID(compact[(i%n)*IndexRowSize:], base)
		}
	})
}

func TestIndexLowerAcrossMergedHourSegments(t *testing.T) {
	const base = int64(1792584000000)
	b := branchStore{rows: make([]byte, 6*IndexRowSize)}
	for hour := 0; hour < 3; hour++ {
		at := base + int64(hour)*3600000
		b.runs = append(b.runs, Run{Start: uint64(hour * 2), End: uint64(hour*2 + 2), MinMS: at + 100, MaxMS: at + 200, PAC: &StoredPAC{Placement: PACPlacement{From: at}}})
		for j := 0; j < 2; j++ {
			id := [16]byte(uuid(at+int64(j+1)*100, uint32(j)).Bytes)
			if e := packIndexID(b.rows[(hour*2+j)*IndexRowSize:], id, at); e != nil {
				t.Fatal(e)
			}
		}
	}
	for start := uint64(0); start < 6; start++ {
		for end := start + 1; end <= 6; end++ {
			for _, delta := range []int64{0, 100, 101, 200, 201, 3600000, 3600100, 3600101, 7200100, 7200201} {
				want := start
				for want < end && b.rowTime(want) < base+delta {
					want++
				}
				if got := b.lower(start, end, base+delta); got != want {
					t.Fatalf("[%d,%d) %d: got %d want %d", start, end, delta, got, want)
				}
			}
		}
	}
}

// Compare references under the same segment layout, without disk, transport, or
// Tantivy query work. Context reconstruction matters for random object lookups.
func BenchmarkRecordReference(b *testing.B) {
	const segments = 1024
	const records = 1024
	const base = int64(1792584000000)
	source := branchStore{rows: make([]byte, segments*(records+1)*IndexRowSize)}
	for i := 0; i < segments; i++ {
		start := uint64(i*(records+1) + 1)
		epoch := base + int64(i)*3600000
		source.runs = append(source.runs, Run{Start: start, End: start + records, PAC: &StoredPAC{Placement: PACPlacement{From: epoch}}})
		for j := 0; j < records; j++ {
			_ = packIndexID(source.row(start+uint64(j)), [16]byte(uuid(epoch+int64(j), uint32(j)).Bytes), epoch)
		}
	}
	for _, random := range []bool{false, true} {
		pattern := "sequential"
		if random {
			pattern = "random"
		}
		for _, compact := range []bool{false, true} {
			name := "segment_ref8"
			if compact {
				name = "record_ref4"
			}
			b.Run(pattern+"/"+name, func(b *testing.B) {
				var context recordContext
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					n := i % (segments * records)
					if random {
						n = (i * 7919) % (segments * records)
					}
					segment, ordinal := n/records, n%records
					var row uint64
					var epoch int64
					if compact {
						row = uint64(segment*(records+1) + 1 + ordinal)
						epoch = context.segment(&source, row).PAC.Placement.From
					} else {
						ref := uint64(segment)<<32 | uint64(ordinal)
						run := source.runs[uint32(ref>>32)]
						row = run.Start + uint64(uint32(ref))
						epoch = run.PAC.Placement.From
					}
					benchUUID = unpackIndexID(source.row(row), epoch)
				}
			})
		}
	}
}
