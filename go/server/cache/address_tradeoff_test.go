package cache

// Controlled experiments only: neither candidate changes the serving format.
import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/fxamacker/cbor/v2"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

var searchDecoder, _ = (cbor.DecOptions{MaxArrayElements: 16 << 20}).DecMode()

type addressFixture struct {
	Fields  []string    `cbor:"fields"`
	Docs    []searchDoc `cbor:"docs"`
	Headers []uint64    `cbor:"headers"`
}

type addressCase struct {
	name              string
	count, perSegment int
	sparse            bool
}

var addressCases = []addressCase{
	{"dense50k", 50000, 1024, false}, {"dense250k", 250000, 1024, false},
	{"sparse250k", 250000, 1024, true}, {"dense250k_small_segments", 250000, 128, false},
}

// Generates production PIL/PACs and production extracted Tantivy cells once.
// Both candidates receive identical member payloads and index input values.
func TestAddressTradeoffFixture(t *testing.T) {
	root := os.Getenv("TRUNK_ADDRESS_BENCH")
	if root == "" {
		t.Skip("set TRUNK_ADDRESS_BENCH to generate experiment fixtures")
	}
	for _, c := range addressCases {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(root, c.name)
			mustAddress(t, os.MkdirAll(dir, 0755))
			l, _ := pacWorkload(t, 0, c.sparse)
			fields := make([]string, len(l.Definition.Fields))
			for k, f := range l.Definition.Fields {
				fields[k] = searchKind(f.Type, len(f.Values) > 0)
			}
			fields[l.Event] = "x"
			fixture := addressFixture{Fields: fields}
			nObjects := (c.count + 8) / 9 // Nine revisions/object, interleaved across hours.
			objects := make([][16]byte, nObjects)
			for i := range objects {
				h := sha256.Sum256([]byte(fmt.Sprintf("object-%d", i)))
				copy(objects[i][:], h[:16])
				objects[i][6] = objects[i][6]&15 | 64
				objects[i][8] = objects[i][8]&63 | 128
			}
			compact, full := []byte{}, []byte{}
			var payloadBytes, gzipBytes, pilBytes int64
			var placements []PACPlacement
			var collectionPosts [2][]collectionPosting
			const epoch = int64(1790002800000) // UTC hour boundary
			for start := 0; start < c.count; start += c.perSegment {
				end := min(c.count, start+c.perSegment)
				hour := start / c.perSegment
				base := epoch + int64(hour)*3600000
				header := uint64(len(compact) / 16)
				compact = append(compact, indexHeader(base)...)
				full = append(full, make([]byte, 20)...)
				copy(full[len(full)-20:], "IDX0") // Preserve segment boundary/sentinel for payload ends.
				frames := make([][]byte, 0, end-start)
				for i := start; i < end; i++ {
					id := [16]byte(uuid(base+int64(i-start)*3000000/int64(c.perSegment), 0).Bytes)
					h := sha256.Sum256([]byte(fmt.Sprintf("event-%d", i)))
					copy(id[6:], h[6:16])
					id[6] = id[6]&15 | 112
					id[8] = id[8]&63 | 128
					word := "completed"
					if i%100 == 1 {
						word = "timeout"
					}
					v := []Value{{Direct, id[:]}, {Direct, objects[i%nObjects][:]}, Uint(uint64(i/nObjects), 4), Uint(uint64(i%60000), 2), Uint(uint64(i%3), 1), Uint(0, 1), Uint(uint64(i%2), 1), Text(fmt.Sprintf("Transaction %d %s at payment provider %d", i, word, i%7)), Uint(uint64(i*101), 4), Uint(uint64(i%2), 1), Text(fmt.Sprintf("Customer request %d", i)), Uint(uint64(i%32768), 2)}
					if c.sparse {
						for k := 3; k < len(v); k++ {
							v[k] = Value{}
						}
						if i%2 == 0 {
							v[4] = Uint(uint64(i%3), 1)
						} else {
							v[7] = Text(fmt.Sprintf("Customer request %d %s", i, word))
							v[8] = Uint(uint64(i*101), 4)
						}
					}
					raw, e := l.Encode(nil, v)
					mustAddress(t, e)
					frames = append(frames, raw)
					pilBytes += int64(len(raw))
					row := uint64(len(compact) / 16)
					doc, e := searchDocument(l, row, raw)
					mustAddress(t, e)
					doc.SourceRow = row
					fixture.Docs = append(fixture.Docs, doc)
					fixture.Headers = append(fixture.Headers, header)
					r := make([]byte, 16)
					mustAddress(t, benchmarkPackUUID(r, id, base))
					compact = append(compact, r...)
					r = make([]byte, 20)
					copy(r, id[:])
					full = append(full, r...)
					collectionPosts[0] = append(collectionPosts[0], collectionPosting{objects[i%nObjects], uint64(i / nObjects), collectionAddress{uint32(header), uint32(row - header)}})
					collectionPosts[1] = append(collectionPosts[1], collectionPosting{objects[i%nObjects], uint64(i / nObjects), collectionAddress{0, uint32(row)}})
				}
				p, e := factorPAC(l, frames, nil, true)
				mustAddress(t, e)
				var memberStart uint64
				if len(placements) > 0 {
					last := placements[len(placements)-1]
					memberStart = last.MemberStart + last.MemberBytes
				}
				placement, e := PlacePAC(p, PACPlacement{PageSize: NativePageSize, Block: uint32(hour), From: base, To: base + 3600000, FirstRow: header + 1, MemberStart: memberStart})
				mustAddress(t, e)
				placements = append(placements, placement)
				ext, e := placement.Extension()
				mustAddress(t, e)
				p.Extensions = []PACExtension{ext}
				p.Bytes, e = p.Marshal()
				mustAddress(t, e)
				payloadBytes += int64(len(p.Bytes))
				z, e := SealGzipLevel(p.Bytes, 1)
				mustAddress(t, e)
				gzipBytes += int64(len(z.Bytes()))
				var cumulative uint32
				for j, m := range p.Members {
					cumulative += uint32(len(m))
					row := int(header) + j + 1
					order.PutUint32(compact[row*16+12:], cumulative)
					order.PutUint32(full[row*20+16:], cumulative)
				}
			}
			for i, d := range fixture.Docs {
				h := fixture.Headers[i]
				at := int(d.Row)
				id := benchmarkUnpackUUID(compact[at*16:], int64(order.Uint64(compact[int(h)*16+4:])))
				if !bytes.Equal(id[:], full[at*20:at*20+16]) {
					t.Fatal("UUID round trip")
				}
			}
			mustAddress(t, os.WriteFile(filepath.Join(dir, "compact.idx"), compact, 0644))
			mustAddress(t, os.WriteFile(filepath.Join(dir, "full.idx"), full, 0644))
			raw, e := encoder.Marshal(fixture)
			mustAddress(t, e)
			mustAddress(t, os.WriteFile(filepath.Join(dir, "fixture.cbor"), raw, 0644))
			collectionBytes := []int64{}
			buildMillis := []float64{}
			for variant, posts := range collectionPosts {
				f, e := os.Create(filepath.Join(dir, fmt.Sprintf("collection%d.idx", variant)))
				mustAddress(t, e)
				idx := newObjectIndex(f)
				begin := time.Now()
				for a := 0; a < len(posts); a += 8192 {
					b := min(a+8192, len(posts))
					mustAddress(t, idx.mergeRetained(objectBinding{}, 2, uint64(len(compact)/16), posts[a:b]))
				}
				buildMillis = append(buildMillis, float64(time.Since(begin).Nanoseconds())/1e6)
				st, e := f.Stat()
				mustAddress(t, e)
				collectionBytes = append(collectionBytes, st.Size())
				// Verify every object revision through the actual on-disk page implementation.
				for i, p := range posts {
					a, ok, e := idx.lookup(p.Object[:], objectBinding{}, p.Revision)
					mustAddress(t, e)
					if !ok || a != p.Address {
						t.Fatalf("collection lookup %d", i)
					}
				}
				mustAddress(t, f.Close())
			}
			// Production pages have 8-byte reference slots in both candidates.
			if collectionBytes[0] != collectionBytes[1] {
				t.Fatal("same page topology changed size")
			}
			result := map[string]any{"events": c.count, "segments": len(placements), "per_segment": c.perSegment, "objects": nObjects, "compact_idx": len(compact), "full_idx": len(full), "shared_pil_bytes": pilBytes, "shared_raw_pac_bytes": payloadBytes, "shared_gzip_pac_bytes": gzipBytes, "collection_bytes": collectionBytes, "collection_build_ms": buildMillis}
			raw, e = json.MarshalIndent(result, "", "  ")
			mustAddress(t, e)
			mustAddress(t, os.WriteFile(filepath.Join(dir, "fixture.json"), raw, 0644))
			t.Log(string(raw))
		})
	}
}
func mustAddress(t testing.TB, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}

// The in-memory read comparison includes header lookup for compact tuples,
// the current compact/direct-row directory control, and full-UUID direct rows.
func BenchmarkAddressTradeoff(b *testing.B) {
	const n = 1 << 20
	for _, per := range []int{128, 4096} {
		compact := make([]byte, (n+n/per)*16)
		full := make([]byte, (n+n/per)*20)
		refs := make([]uint64, n)
		direct := make([]uint32, n)
		runs := make([]Run, 0, n/per)
		for i := 0; i < n; i++ {
			h := (i / per) * (per + 1)
			row := h + 1 + i%per
			base := int64(1790002800000) + int64(i/per)*3600000
			if i%per == 0 {
				copy(compact[h*16:], indexHeader(base))
				runs = append(runs, Run{Start: uint64(h + 1), End: uint64(h + per + 1), PAC: &StoredPAC{Placement: PACPlacement{From: base}}})
			}
			id := [16]byte(uuid(base+int64(i%per), uint32(i)).Bytes)
			mustAddress(b, benchmarkPackUUID(compact[row*16:], id, base))
			copy(full[row*20:], id[:])
			order.PutUint32(compact[row*16+12:], uint32(i%per+1)*80)
			order.PutUint32(full[row*20+16:], uint32(i%per+1)*80)
			refs[i] = uint64(h)<<24 | uint64(i%per+1)
			direct[i] = uint32(row)
		}
		source := branchStore{rows: compact, runs: runs}
		for _, pattern := range []string{"sequential", "scattered"} {
			for _, mode := range []string{"compact_tuple", "compact_tuple_cached", "compact_direct_control", "full_direct", "compact_tuple_pac_context", "full_direct_pac_context"} {
				b.Run(fmt.Sprintf("%d/%s/%s", per, pattern, mode), func(b *testing.B) {
					var ctx recordContext
					last := uint64(^uint64(0))
					var base int64
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						j := i & (n - 1)
						if pattern == "scattered" {
							j = (i * 7919) & (n - 1)
						}
						switch mode {
						case "compact_tuple", "compact_tuple_cached", "compact_tuple_pac_context":
							ref := refs[j]
							header := ref >> 24
							row := header + (ref & 0xffffff)
							if mode == "compact_tuple_pac_context" {
								benchOrdinal = int(ctx.segment(&source, row).Start)
							}
							if mode == "compact_tuple" || header != last {
								base = int64(order.Uint64(compact[header*16+4:]))
								last = header
							}
							benchUUID = benchmarkUnpackUUID(compact[row*16:], base)
							benchLength = order.Uint32(compact[row*16+12:]) - order.Uint32(compact[row*16-4:])
						case "compact_direct_control":
							row := uint64(direct[j])
							base = ctx.segment(&source, row).PAC.Placement.From
							benchUUID = benchmarkUnpackUUID(compact[row*16:], base)
							benchLength = order.Uint32(compact[row*16+12:]) - order.Uint32(compact[row*16-4:])
						case "full_direct", "full_direct_pac_context":
							row := int(direct[j])
							if mode == "full_direct_pac_context" {
								benchOrdinal = int(ctx.segment(&source, uint64(row)).Start)
							}
							benchUUID = [16]byte(full[row*20 : row*20+16])
							benchLength = order.Uint32(full[row*20+16:]) - order.Uint32(full[row*20-4:])
						}
					}
				})
			}
		}
		for _, mode := range []string{"compact", "full", "full_byte_loop_control"} {
			b.Run(fmt.Sprintf("%d/binary_search/%s", per, mode), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					seg := (i * 13) % (n / per)
					start := seg*(per+1) + 1
					delta := int64((i * 71) % per)
					base := int64(1790002800000) + int64(seg)*3600000
					if mode == "compact" {
						benchOrdinal = sort.Search(per, func(k int) bool { return benchmarkUUIDDelta(compact[(start+k)*16:]) >= delta })
					} else if mode == "full" {
						target := uint64(base + delta)
						benchOrdinal = sort.Search(per, func(k int) bool { return binary.BigEndian.Uint64(full[(start+k)*20:])>>16 >= target })
					} else {
						benchOrdinal = sort.Search(per, func(k int) bool { return eventTime(full[(start+k)*20:]) >= base+delta })
					}
				}
			})
		}
	}
}

// Repacked immutable leaf-page experiment, NOT a replacement insertion engine.
// Same 4 KiB pages, 37-byte header, pointer array and 32-byte units. It measures
// the actual extra density possible only after changing reference slot widths.
func TestAddressTradeoffPackedCollection(t *testing.T) {
	root := os.Getenv("TRUNK_ADDRESS_BENCH")
	if root == "" {
		t.Skip("opt-in experiment")
	}
	for _, c := range addressCases {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(root, c.name)
			raw, e := os.ReadFile(filepath.Join(dir, "fixture.cbor"))
			mustAddress(t, e)
			var f addressFixture
			mustAddress(t, searchDecoder.Unmarshal(raw, &f))
			count := (c.count + 8) / 9
			type object struct {
				id           [16]byte
				rows, tuples []uint64
			}
			objects := make([]object, count)
			for i := range objects {
				h := sha256.Sum256([]byte(fmt.Sprintf("object-%d", i)))
				copy(objects[i].id[:], h[:16])
				objects[i].id[6] = objects[i].id[6]&15 | 64
				objects[i].id[8] = objects[i].id[8]&63 | 128
			}
			for i, d := range f.Docs {
				o := &objects[i%count]
				o.rows = append(o.rows, uint64(d.Row))
				h := f.Headers[i]
				o.tuples = append(o.tuples, h<<24|(uint64(d.Row)-h))
			}
			sort.Slice(objects, func(i, j int) bool { return bytes.Compare(objects[i].id[:], objects[j].id[:]) < 0 })
			file, e := os.Open(filepath.Join(dir, "collection0.idx"))
			mustAddress(t, e)
			current, e := loadObjectIndex(file)
			mustAddress(t, e)
			stats := map[string]int{"current_leaf_pages": len(current.sources[objectBinding{}].Fences), "current_directory_pages": len(current.view.Metadata)}
			data, e := json.MarshalIndent(stats, "", "  ")
			mustAddress(t, e)
			mustAddress(t, os.WriteFile(filepath.Join(dir, "collection_publication.json"), data, 0644))
			mustAddress(t, file.Close())
			sizes := map[int]map[string]int{}
			for _, width := range []int{4, 7, 8} {
				parentN, extraN := 8/width, 32/width
				p := newCollectionPage(objectBinding{})
				pages := []collectionPage{}
				fences := [][16]byte{}
				for _, o := range objects {
					units := 1 + (max(0, len(o.rows)-parentN)+extraN-1)/extraN
					if p.used()+units > collectionUnits {
						p.seal()
						pages = append(pages, p)
						p = newCollectionPage(objectBinding{})
					}
					if p.used() == 0 {
						fences = append(fences, o.id)
					}
					n := p.used()
					for j := 0; j < units; j++ {
						p.add(p.used(), j > 0)
					}
					copy(p[p.at(n):], o.id[:])
					for j := range o.rows {
						at := p.at(n) + 24 + j*width
						if j >= parentN {
							at = p.at(n+1+(j-parentN)/extraN) + (j-parentN)%extraN*width
						}
						v := o.rows[j]
						if width != 4 {
							v = o.tuples[j]
						}
						for k := 0; k < width; k++ {
							p[at+k] = byte(v >> uint(8*k))
						}
					}
				}
				if p.used() > 0 {
					p.seal()
					pages = append(pages, p)
				}
				// Resolve every revision through sorted page fences and virtual slot pointers.
				for _, o := range objects {
					pi := sort.Search(len(fences), func(i int) bool { return bytes.Compare(fences[i][:], o.id[:]) > 0 }) - 1
					p := &pages[pi]
					n := p.find(o.id[:])
					if n < 0 {
						t.Fatal("missing object")
					}
					for j := range o.rows {
						at := p.at(n) + 24 + j*width
						if j >= parentN {
							at = p.at(n+1+(j-parentN)/extraN) + (j-parentN)%extraN*width
						}
						var v uint64
						for k := 0; k < width; k++ {
							v |= uint64(p[at+k]) << uint(8*k)
						}
						want := o.rows[j]
						if width != 4 {
							want = o.tuples[j]
						}
						if v != want {
							t.Fatal("bad packed reference")
						}
					}
				}
				out, e := os.Create(filepath.Join(dir, fmt.Sprintf("packed%d.pages", width)))
				mustAddress(t, e)
				for i := range pages {
					_, e = out.Write(pages[i][:])
					mustAddress(t, e)
				}
				mustAddress(t, out.Close())
				sizes[width] = map[string]int{"leaf_pages": len(pages), "leaf_bytes": len(pages) * 4096, "packed_fence_bytes": len(fences) * 20}
			}
			raw, e = json.MarshalIndent(sizes, "", "  ")
			mustAddress(t, e)
			mustAddress(t, os.WriteFile(filepath.Join(dir, "packed_collection.json"), raw, 0644))
			t.Log(string(raw))
		})
	}
}

// Actual production collection lookup + UUID reconstruction. Both retain the
// existing 8-byte page slots; this does not claim a packed-page format migration.
func BenchmarkAddressCollectionLookup(b *testing.B) {
	root := os.Getenv("TRUNK_ADDRESS_BENCH")
	if root == "" {
		b.Skip("generate fixtures first")
	}
	dir := filepath.Join(root, "dense250k")
	for variant, mode := range []string{"compact_tuple", "full_direct"} {
		b.Run(mode, func(b *testing.B) {
			file, e := os.Open(filepath.Join(dir, fmt.Sprintf("collection%d.idx", variant)))
			mustAddress(b, e)
			defer file.Close()
			idx, e := loadObjectIndex(file)
			mustAddress(b, e)
			filename := "compact.idx"
			if variant == 1 {
				filename = "full.idx"
			}
			rows, e := os.ReadFile(filepath.Join(dir, filename))
			mustAddress(b, e)
			const objects = (250000 + 8) / 9
			ids := make([][16]byte, objects)
			for i := range ids {
				h := sha256.Sum256([]byte(fmt.Sprintf("object-%d", i)))
				copy(ids[i][:], h[:16])
				ids[i][6] = ids[i][6]&15 | 64
				ids[i][8] = ids[i][8]&63 | 128
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				n := (i * 7919) % objects
				a, ok, e := idx.lookup(ids[n][:], objectBinding{}, 7)
				if e != nil || !ok {
					b.Fatal("lookup", e)
				}
				row := int(uint64(a.Hour) + uint64(a.Record)) // Historical header-row/displacement benchmark only.
				if variant == 0 {
					base := int64(order.Uint64(rows[int(a.Hour)*16+4:]))
					benchUUID = benchmarkUnpackUUID(rows[row*16:], base)
				} else {
					benchUUID = [16]byte(rows[row*20 : row*20+16])
				}
			}
		})
	}
}

// Historical layout for measuring alternatives only; no runtime fallback.
func benchmarkUUIDDelta(row []byte) int64 {
	return int64(uint32(row[0])<<14 | uint32(row[1])<<6 | uint32(row[2])>>2)
}
func benchmarkPackUUID(row []byte, id [16]byte, base int64) error {
	delta := eventTime(id[:]) - base
	hi := uint64(delta)<<42 | uint64(id[6]&15)<<38 | uint64(id[7])<<30 | uint64(id[8]&63)<<24 | uint64(id[9])<<16 | uint64(id[10])<<8 | uint64(id[11])
	for i := 7; i >= 0; i-- {
		row[i] = byte(hi)
		hi >>= 8
	}
	copy(row[8:12], id[12:])
	return nil
}
func benchmarkUnpackUUID(row []byte, base int64) [16]byte {
	var id [16]byte
	ms := base + benchmarkUUIDDelta(row)
	for i := 5; i >= 0; i-- {
		id[i] = byte(ms)
		ms >>= 8
	}
	var hi uint64
	for _, b := range row[:8] {
		hi = hi<<8 | uint64(b)
	}
	id[6] = 0x70 | byte(hi>>38)&15
	id[7] = byte(hi >> 30)
	id[8] = 0x80 | byte(hi>>24)&63
	id[9] = byte(hi >> 16)
	id[10] = byte(hi >> 8)
	id[11] = byte(hi)
	copy(id[12:], row[8:12])
	return id
}
