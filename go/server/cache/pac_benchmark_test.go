package cache

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"testing"
)

// Measures compression alone on already packed, 1,000-record PACs. Decoding is
// gzip-only (no PIL validation/hydration), with a reused reader like a warm stream.
func BenchmarkPACCompressionLevels(b *testing.B) {
	for _, sparse := range []bool{false, true} {
		kind := "dense12"
		if sparse {
			kind = "sparse1to2"
		}
		l, frames := pacWorkload(b, 1000, sparse)
		p, err := PackPAC(l, frames, nil, true)
		if err != nil {
			b.Fatal(err)
		}
		for _, level := range []int{1, 3, 6, 9} {
			sealed, err := SealGzipLevel(p.Bytes, level)
			if err != nil {
				b.Fatal(err)
			}
			name := fmt.Sprintf("%s/level%d", kind, level)
			b.Run(name+"/seal", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(p.Bytes)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := SealGzipLevel(p.Bytes, level); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(len(sealed.Bytes())), "encoded-bytes/PAC")
				b.ReportMetric(float64(len(p.Bytes)), "decoded-bytes/PAC")
			})
			b.Run(name+"/decode", func(b *testing.B) {
				src := bytes.NewReader(sealed.Bytes())
				z, err := gzip.NewReader(src)
				if err != nil {
					b.Fatal(err)
				}
				defer z.Close()
				b.ReportAllocs()
				b.SetBytes(int64(len(p.Bytes)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					src.Reset(sealed.Bytes())
					if err := z.Reset(src); err != nil {
						b.Fatal(err)
					}
					if n, err := io.Copy(io.Discard, z); err != nil || n != int64(len(p.Bytes)) {
						b.Fatal("decoded length mismatch", n, err)
					}
				}
			})
		}
	}
}

func pacWorkload(b testing.TB, n int, sparse bool) (*Layout, [][]byte) {
	b.Helper()
	l, err := Compile(Definition{Version: 1 << 20, Identity: "payment/transactions", Fields: []Field{
		{Name: "event_id", Type: "UUID"}, {Name: "id", Type: "UUID"}, {Name: "revision_id", Type: "U32"},
		{Name: "duration", Type: "U16"}, {Name: "state", Type: "STRING", Values: []string{"pending", "authorized", "canceled"}},
		{Name: "service", Type: "STRING", Values: []string{"payment", "customer"}}, {Name: "region", Type: "STRING", Values: []string{"us-east", "us-west"}},
		{Name: "message", Type: "STRING"}, {Name: "amount", Type: "U32"}, {Name: "retry", Type: "BOOL"},
		{Name: "description", Type: "STRING"}, {Name: "delta", Type: "I16"},
	}, Resource: Binding{Major: 1, Format: Format, Service: "payment", Resource: "transactions", Timezone: "UTC", Roles: Roles{Event: "event_id", Object: "id", Revision: "revision_id"}}})
	if err != nil {
		b.Fatal(err)
	}
	frames := make([][]byte, n)
	for i := range frames {
		values := []Value{uuid(1790000000000+int64(i), uint32(i)), uuid(1780000000000, uint32(i%401)), Uint(uint64(i), 4), Uint(uint64(i%60000), 2), Uint(uint64(i%3), 1), Uint(0, 1), Uint(0, 1), Text(fmt.Sprintf("Transaction %d authorized by payment provider %d", i, i%7)), Uint(uint64(i*101), 4), Uint(uint64(i%2), 1), Text("Online purchase"), Uint(uint64(i%32768), 2)}
		if sparse {
			for j := 3; j < len(values); j++ {
				values[j] = Value{}
			}
			if i%2 == 0 {
				values[4] = Uint(uint64(i%3), 1)
			} else {
				values[10] = Text(fmt.Sprintf("Customer note %d", i))
				values[8] = Uint(uint64(i*101), 4)
			}
		}
		frames[i], err = l.Encode(nil, values)
		if err != nil {
			b.Fatal(err)
		}
	}
	return l, frames
}
func BenchmarkPACWorkloads(b *testing.B) {
	for _, sparse := range []bool{false, true} {
		kind := "dense12"
		if sparse {
			kind = "sparse1to2"
		}
		for _, n := range []int{50, 1000, 10000} {
			l, frames := pacWorkload(b, n, sparse)
			for _, prefix := range []bool{false, true} {
				mode := "plain"
				if prefix {
					mode = "prefix"
				}
				b.Run(fmt.Sprintf("%s/%d/%s", kind, n, mode), func(b *testing.B) {
					p, err := PackPAC(l, frames, nil, prefix)
					if err != nil {
						b.Fatal(err)
					}
					sealed, err := SealGzip(p.Bytes)
					if err != nil {
						b.Fatal(err)
					}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						if _, err = PackPAC(l, frames, nil, prefix); err != nil {
							b.Fatal(err)
						}
					}
					b.ReportMetric(float64(len(p.Bytes))/float64(n), "bytes/record")
					b.ReportMetric(float64(len(sealed.Bytes()))/float64(n), "gzip-bytes/record")
				})
			}
		}
	}
}

func BenchmarkPACValidation(b *testing.B) {
	for _, n := range []int{50, 1000, 10000} {
		l, frames := pacWorkload(b, n, false)
		for _, prefix := range []bool{false, true} {
			p, err := PackPAC(l, frames, nil, prefix)
			if err != nil {
				b.Fatal(err)
			}
			mode := "plain"
			if prefix {
				mode = "prefix"
			}
			b.Run(fmt.Sprintf("%d/%s", n, mode), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := ParsePAC(l, p.Bytes, nil); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
