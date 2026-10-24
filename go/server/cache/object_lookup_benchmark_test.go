package cache

// Opt-in comparison over identical sealed data. No serving format is changed.
import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type objectLookupCase struct {
	id        [16]byte
	text      string
	rows      []uint64
	revision7 uint64
}

func objectLookupFixture(t testing.TB, name string) (string, addressFixture, []objectLookupCase) {
	t.Helper()
	root := os.Getenv("TRUNK_ADDRESS_BENCH")
	if root == "" {
		t.Skip("TRUNK_ADDRESS_BENCH fixture required")
	}
	dir := filepath.Join(root, name)
	raw, err := os.ReadFile(filepath.Join(dir, "fixture.cbor"))
	mustAddress(t, err)
	var f addressFixture
	mustAddress(t, searchDecoder.Unmarshal(raw, &f))
	positions := map[string]int{}
	var objects []objectLookupCase
	for _, d := range f.Docs {
		var id string
		var rev uint64
		for _, c := range d.Cells {
			if c.K == 1 {
				id = c.E[0]
			}
			if c.K == 2 {
				rev = c.U[0]
			}
		}
		n, ok := positions[id]
		if !ok {
			n = len(objects)
			positions[id] = n
			b, e := hex.DecodeString(id)
			mustAddress(t, e)
			objects = append(objects, objectLookupCase{text: id, id: [16]byte(b)})
		}
		objects[n].rows = append(objects[n].rows, uint64(d.Row))
		if rev == 7 {
			objects[n].revision7 = uint64(d.Row)
		}
	}
	return dir, f, objects
}

func objectRequest(dir string, f addressFixture, id string, kind string) searchRequest {
	r := searchRequest{Op: "query", Path: filepath.Join(dir, "tantivy_broad_direct"), Binding: "address-experiment", Fields: f.Fields, Ranges: [][2]uint64{{uint64(f.Docs[0].Row), uint64(f.Docs[len(f.Docs)-1].Row) + 1}}, Filters: []searchFilter{{K: 1, Kind: "e", Op: "==", Value: id}}}
	if kind == "revision7" {
		r.Filters = append(r.Filters, searchFilter{K: 2, Kind: "u", Op: "==", Value: "7"})
	}
	return r
}

// This historical experiment encodes an absolute header row in the first component.
// Production references use hour IDs and resolve through the positional directory.
func objectCollectionRows(idx *objectIndex, id []byte, kind string, out []uint64) ([]uint64, error) {
	out = out[:0]
	if kind == "revision7" {
		a, ok, e := idx.lookup(id, objectBinding{}, 7)
		if ok {
			out = append(out, uint64(a.Hour)+uint64(a.Record))
		}
		return out, e
	}
	err := idx.visit(id, objectBinding{}, func(_ uint64, a collectionAddress) { out = append(out, uint64(a.Hour)+uint64(a.Record)) })
	return out, err
}

// Checks matching identities and revision results, not merely matching hit counts.
func TestObjectLookupComparison(t *testing.T) {
	for _, name := range []string{"dense50k", "dense250k"} {
		t.Run(name, func(t *testing.T) {
			dir, f, objects := objectLookupFixture(t, name)
			file, e := os.Open(filepath.Join(dir, "collection0.idx"))
			mustAddress(t, e)
			defer file.Close()
			idx, e := loadObjectIndex(file)
			mustAddress(t, e)
			for _, kind := range []string{"history", "revision7", "missing"} {
				for n := 0; n < 128; n++ {
					o := objects[n*7919%len(objects)]
					want := o.rows
					if kind == "revision7" {
						want = []uint64{o.revision7}
					}
					if kind == "missing" {
						o.id[0] ^= 0x80
						o.text = hex.EncodeToString(o.id[:])
						want = nil
					}
					got, e := objectCollectionRows(idx, o.id[:], kind, nil)
					mustAddress(t, e)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("collection %s: %v != %v", kind, got, want)
					}
					reply, e := searchCall(context.Background(), objectRequest(dir, f, o.text, kind))
					mustAddress(t, e)
					if len(reply.Rows) != len(want) {
						t.Fatalf("tantivy %s: %v != %v", kind, reply.Rows, want)
					}
					for j := range want {
						if reply.Rows[j] != want[j] {
							t.Fatal("different row references")
						}
					}
				}
			}
		})
	}
}

var objectLookupSink uint64

func BenchmarkObjectLookupComparison(b *testing.B) {
	for _, name := range []string{"dense50k", "dense250k"} {
		b.Run(name, func(b *testing.B) {
			dir, f, objects := objectLookupFixture(b, name)
			file, e := os.Open(filepath.Join(dir, "collection0.idx"))
			mustAddress(b, e)
			defer file.Close()
			idx, e := loadObjectIndex(file)
			mustAddress(b, e)
			for _, kind := range []string{"history", "revision7", "missing"} {
				// Identical scattered UUID schedule; no hashing/UUID rendering in timed section.
				reqs := make([]searchRequest, len(objects))
				ids := make([][16]byte, len(objects))
				for n, o := range objects {
					if kind == "missing" {
						o.id[0] ^= 0x80
						o.text = hex.EncodeToString(o.id[:])
					}
					ids[n] = o.id
					reqs[n] = objectRequest(dir, f, o.text, kind)
				}
				for _, engine := range []string{"collection", "tantivy_native", "tantivy_cancelable"} {
					b.Run(fmt.Sprintf("%s/%s", kind, engine), func(b *testing.B) {
						ctx := context.Background()
						if engine == "tantivy_cancelable" {
							var cancel context.CancelFunc
							ctx, cancel = context.WithCancel(ctx)
							defer cancel()
						}
						if engine != "collection" {
							_, e := searchCall(ctx, reqs[0])
							mustAddress(b, e)
						}
						// Caller-supplied bounded buffer avoids charging collection for slice allocation.
						out := make([]uint64, 0, 16)
						b.ReportAllocs()
						b.ResetTimer()
						for j := 0; j < b.N; j++ {
							n := j * 7919 % len(objects)
							if engine == "collection" {
								var err error
								out, err = objectCollectionRows(idx, ids[n][:], kind, out)
								if err != nil {
									b.Fatal(err)
								}
								objectLookupSink += uint64(len(out))
							} else {
								reply, err := searchCall(ctx, reqs[n])
								if err != nil {
									b.Fatal(err)
								}
								objectLookupSink += uint64(len(reply.Rows))
							}
						}
					})
				}
			}
		})
	}
}
