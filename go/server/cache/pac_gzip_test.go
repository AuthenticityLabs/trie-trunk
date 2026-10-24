package cache

import (
	"bytes"
	"compress/gzip"
	"hash/crc32"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
)

func TestPACGzipSplice(t *testing.T) {
	rng := rand.New(rand.NewPCG(42, 91))
	for _, size := range []int{0, 1, 2, 31, 32768, 65535, 65536, 130001} {
		var wire, expected bytes.Buffer
		w := NewGzipPACWriter(&wire)
		for i := 0; i < 9; i++ {
			raw := make([]byte, size+i)
			for j := range raw {
				if i%2 == 0 {
					raw[j] = byte(rng.Uint32())
				} else {
					raw[j] = byte(j % 7)
				}
			}
			s, err := SealGzipLevel(raw, i+1)
			if err != nil {
				t.Fatal(err)
			}
			before := append([]byte(nil), s.Bytes()...)
			z, err := gzip.NewReader(bytes.NewReader(before))
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := io.ReadAll(z)
			z.Close()
			if err != nil || !bytes.Equal(decoded, raw) {
				t.Fatalf("level %d round trip: %v", i+1, err)
			}
			s, err = RestoreSealedGzip(before, uint64(len(raw)))
			if err != nil {
				t.Fatal(err)
			}
			if i%3 == 0 {
				_, err = w.Write(raw)
			} else {
				err = w.Flush()
				if err == nil {
					err = w.append(s)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, s.Bytes()) {
				t.Fatal("sealed bytes changed")
			}
			expected.Write(raw)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		src := bytes.NewReader(wire.Bytes())
		r, err := gzip.NewReader(src)
		if err != nil {
			t.Fatal(err)
		}
		r.Multistream(false)
		decoded, err := io.ReadAll(r)
		r.Close()
		if err != nil || !bytes.Equal(decoded, expected.Bytes()) || src.Len() != 0 {
			t.Fatalf("single member splice size=%d: %v, trailing=%d", size, err, src.Len())
		}
	}
	// The CRC concatenation identity holds without looking at either payload.
	a, b := []byte("header bytes"), []byte("PAC member bytes")
	if combineCRC32(crc32.ChecksumIEEE(a), crc32.ChecksumIEEE(b), uint64(len(b))) != crc32.ChecksumIEEE(append(a, b...)) {
		t.Fatal("CRC concatenation")
	}
	// A small fixture lets browser Fetch, not just Go/zlib, verify this path.
	var out bytes.Buffer
	w := NewGzipPACWriter(&out)
	for _, part := range []string{"first", "second"} {
		s, err := SealGzip([]byte(part))
		if err != nil {
			t.Fatal(err)
		}
		if err = w.append(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if dir := os.Getenv("PIL_PAC_FIXTURE_DIR"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, "spliced.gz"), out.Bytes(), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPACGzipInvalidLevel(t *testing.T) {
	for _, level := range []int{-2, -1, 0, 10} {
		if _, err := SealGzipLevel([]byte("test"), level); err == nil {
			t.Fatalf("accepted level %d", level)
		}
	}
}
func BenchmarkPACGzipDelivery(b *testing.B) {
	l, frames := pacWorkload(b, 1000, false)
	p, err := PackPAC(l, frames, nil, true)
	if err != nil {
		b.Fatal(err)
	}
	sealed, err := SealGzip(p.Bytes)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("reuse4", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(4 * len(p.Bytes)))
		var out bytes.Buffer
		out.Grow(4*len(sealed.Bytes()) + 32)
		for i := 0; i < b.N; i++ {
			out.Reset()
			w := NewGzipPACWriter(&out)
			for j := 0; j < 4; j++ {
				if err := w.append(sealed); err != nil {
					b.Fatal(err)
				}
			}
			if err := w.Close(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("compress4", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(4 * len(p.Bytes)))
		var out bytes.Buffer
		out.Grow(4*len(sealed.Bytes()) + 32)
		for i := 0; i < b.N; i++ {
			out.Reset()
			w := fastGzipWriters.Get().(*gzip.Writer)
			w.Reset(&out)
			for j := 0; j < 4; j++ {
				w.Write(p.Bytes)
			}
			w.Close()
			fastGzipWriters.Put(w)
		}
	})
}
