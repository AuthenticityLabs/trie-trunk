package cache

// Address-layout experiment only. No production format or identity contract is
// changed until the fixed-segment capacity and browser quota tradeoff is settled.
import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
	"testing"
)

const experimentalCollectionWidth = 26 // UUID16 | revision U16 | segment U32 | record U32
var experimentalAddressSink uint64

func experimentObject(n int) (id [16]byte) {
	id[6] = 0x40
	id[8] = 0x80
	binary.BigEndian.PutUint64(id[8:], uint64(n+1)|(uint64(1)<<63))
	return
}
func experimentRow(dst []byte, id [16]byte, revision uint32, segment, record uint32) error {
	if revision > 65535 || record == 0 {
		return fmt.Errorf("revision/record overflow")
	}
	copy(dst, id[:])
	binary.BigEndian.PutUint16(dst[16:], uint16(revision))
	binary.LittleEndian.PutUint32(dst[18:], segment)
	binary.LittleEndian.PutUint32(dst[22:], record)
	return nil
}
func experimentFind(rows []byte, id [16]byte, revision uint16) (uint32, uint32, bool) {
	var key [18]byte
	copy(key[:], id[:])
	binary.BigEndian.PutUint16(key[16:], revision)
	n := len(rows) / experimentalCollectionWidth
	at := sort.Search(n, func(i int) bool {
		return bytes.Compare(rows[i*experimentalCollectionWidth:i*experimentalCollectionWidth+18], key[:]) >= 0
	})
	if at == n || !bytes.Equal(rows[at*experimentalCollectionWidth:at*experimentalCollectionWidth+18], key[:]) {
		return 0, 0, false
	}
	row := rows[at*experimentalCollectionWidth:]
	return binary.LittleEndian.Uint32(row[18:]), binary.LittleEndian.Uint32(row[22:]), true
}
func TestFixedSlotReferenceExperiment(t *testing.T) {
	rows := make([]byte, 4*experimentalCollectionWidth)
	for i, r := range []uint32{0, 1, 32768, 65535} {
		if e := experimentRow(rows[i*experimentalCollectionWidth:], experimentObject(0), r, 17, r+1); e != nil {
			t.Fatal(e)
		}
	}
	for _, r := range []uint16{0, 1, 32768, 65535} {
		segment, record, ok := experimentFind(rows, experimentObject(0), r)
		if !ok || segment != 17 || record != uint32(r)+1 {
			t.Fatal(segment, record, ok)
		}
	}
	if _, _, ok := experimentFind(rows, experimentObject(0), 2); ok {
		t.Fatal("revision gap resolved")
	}
	if _, _, ok := experimentFind(rows, experimentObject(1), 1); ok {
		t.Fatal("unknown object resolved")
	}
	if e := experimentRow(rows, experimentObject(0), 65536, 0, 1); e == nil {
		t.Fatal("revision overflow wrapped")
	}

}
func BenchmarkFixedCollectionExperiment(b *testing.B) {
	const objects = 256
	const revisions = 512
	rows := make([]byte, objects*revisions*experimentalCollectionWidth)
	ids := make([][16]byte, objects)
	addresses := make([][]byte, objects)
	for obj := range ids {
		ids[obj] = experimentObject(obj)
		addresses[obj] = make([]byte, revisions*8)
		for rev := 0; rev < revisions; rev++ {
			at := obj*revisions + rev
			_ = experimentRow(rows[at*experimentalCollectionWidth:], ids[obj], uint32(rev), uint32(obj), uint32(rev+1))
			binary.LittleEndian.PutUint32(addresses[obj][rev*8:], uint32(obj))
			binary.LittleEndian.PutUint32(addresses[obj][rev*8+4:], uint32(rev+1))
		}
	}
	for _, mode := range []string{"packed_26B_binary", "object_directory_then_revision_slot"} {
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			var state uint32 = 7
			for i := 0; i < b.N; i++ {
				state = state*1664525 + 1013904223
				obj := int((state >> 16) % objects)
				rev := uint16(state % revisions)
				id := ids[obj]
				if mode == "packed_26B_binary" {
					s, r, ok := experimentFind(rows, id, rev)
					if !ok {
						b.Fatal("missing")
					}
					experimentalAddressSink = uint64(s)<<32 | uint64(r)
				} else {
					n := sort.Search(objects, func(i int) bool { return bytes.Compare(ids[i][:], id[:]) >= 0 })
					if n == objects || ids[n] != id {
						b.Fatal("missing")
					}
					p := addresses[n][int(rev)*8:]
					experimentalAddressSink = binary.LittleEndian.Uint64(p)
				}
			}
		})
	}
}
func BenchmarkFixedSegmentAddressExperiment(b *testing.B) {
	const count = 8192
	var branch branchStore
	for i := 0; i < count; i++ {
		branch.runs = append(branch.runs, Run{Start: uint64(i*513 + 1), End: uint64((i + 1) * 513), PodsStart: uint64(i) * (64 << 20)})
	}
	for _, mode := range []string{"current_scattered_directory", "fixed_segment_arithmetic"} {
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			var c recordContext
			var state uint32 = 1
			for i := 0; i < b.N; i++ {
				state = state*1664525 + 1013904223
				segment := uint64((state >> 16) % count)
				record := uint64(state%512 + 1)
				if mode == "current_scattered_directory" {
					experimentalAddressSink = c.segment(&branch, segment*513+record).PodsStart + record*16
				} else {
					experimentalAddressSink = segment*(64<<20) + record*16
				}
			}
		})
	}
}
