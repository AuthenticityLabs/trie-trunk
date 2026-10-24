package cache

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPACStatePreservationAndExtensions(t *testing.T) {
	for _, adaptive := range []string{"none", "16", "32"} {
		t.Run(adaptive, func(t *testing.T) {
			d := testLayout(t, true).Definition
			d.AdaptiveIntegers = adaptive
			if adaptive != "none" {
				d.PIL = 1
			}
			l, err := Compile(d)
			if err != nil {
				t.Fatal(err)
			}
			var frames [][]byte
			for i := 0; i < 12; i++ {
				frames = append(frames, testFrame(t, l, time.UnixMilli(int64(i)), uint32(i), i%3 == 0))
			}
			p, err := PackPAC(l, frames, []PACExtension{{Type: 99, Data: []byte("optional")}}, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Common) == 0 {
				t.Fatal("no prefix")
			}
			var c Frame
			if err = l.Scan(p.Common, &c); err != nil {
				t.Fatal(err)
			}
			if c.Values[3].State != Absent || c.Values[5].State != Absent {
				t.Fatal("sparse/NULL states were factored incorrectly")
			}
			decoded, err := ParsePAC(l, p.Bytes, nil)
			if err != nil {
				t.Fatal(err)
			}
			for i, m := range decoded.Members {
				full, e := ResolvePAC(l, decoded.Common, m)
				if e != nil || !bytes.Equal(full, frames[i]) {
					t.Fatalf("state/value round trip %d: %v", i, e)
				}
			}
			required, err := PackPAC(l, frames, []PACExtension{{Type: 99, Required: true}}, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ParsePAC(l, required.Bytes, nil); err == nil {
				t.Fatal("unknown required extension accepted")
			}
			if _, err = ParsePAC(l, required.Bytes, map[uint32]bool{99: true}); err != nil {
				t.Fatal(err)
			}
			if _, err = PackPAC(l, frames, []PACExtension{{Type: 1}, {Type: 1}}, false); err == nil {
				t.Fatal("duplicate extension")
			}
			for n := 0; n < len(p.Bytes); n++ {
				if _, err = ParsePAC(l, p.Bytes[:n], nil); err == nil {
					t.Fatal("truncation accepted", n)
				}
			}
			damaged := append([]byte(nil), p.Bytes...)
			damaged[32] ^= 1
			if _, err = ParsePAC(l, damaged, nil); err == nil {
				t.Fatal("definition mismatch accepted")
			}
			// A common field cannot appear again in a member, even with equal bytes.
			broken := *p
			broken.Members = append([][]byte(nil), p.Members...)
			broken.Members[0] = frames[0]
			raw, _ := broken.marshal()
			if _, err = ParsePAC(l, raw, nil); err == nil {
				t.Fatal("common override accepted")
			}
		})
	}
}

func TestPACGzipReuseAndFixture(t *testing.T) {
	l := testLayout(t, false)
	var frames [][]byte
	for i := 0; i < 16; i++ {
		frames = append(frames, testFrame(t, l, time.UnixMilli(int64(i)), uint32(i), false))
	}
	p, e := PackPAC(l, frames, []PACExtension{{Type: PACSource, Data: []byte("demo/payment/transactions.v1/write")}}, true)
	if e != nil {
		t.Fatal(e)
	}
	sealed, e := SealGzip(p.Bytes)
	if e != nil {
		t.Fatal(e)
	}
	encoded := bytes.NewBuffer(sealed.Bytes())
	var spliced bytes.Buffer
	splice := NewGzipPACWriter(&spliced)
	for i := 0; i < 2; i++ {
		if e = splice.append(sealed); e != nil {
			t.Fatal(e)
		}
	}
	if e = splice.Close(); e != nil {
		t.Fatal(e)
	}
	// Reuse complete stored members; gzip decoding must yield two delimited PACs.
	joined := append(append([]byte(nil), encoded.Bytes()...), encoded.Bytes()...)
	r, e := gzip.NewReader(bytes.NewReader(joined))
	if e != nil {
		t.Fatal(e)
	}
	plain, e := io.ReadAll(r)
	if e != nil {
		t.Fatal(e)
	}
	r.Close()
	if !bytes.Equal(plain, append(append([]byte(nil), p.Bytes...), p.Bytes...)) {
		t.Fatal("multi-member gzip changed PACs")
	}
	if dir := os.Getenv("PIL_PAC_FIXTURE_DIR"); dir != "" {
		if e = os.MkdirAll(dir, 0755); e != nil {
			t.Fatal(e)
		}
		for name, raw := range map[string][]byte{"packed.pac": p.Bytes, "packed.pac.gz": encoded.Bytes(), "spliced-pacs.gz": spliced.Bytes(), "definition.cbor": l.Bytes} {
			if e = os.WriteFile(filepath.Join(dir, name), raw, 0644); e != nil {
				t.Fatal(e)
			}
		}
	}
}

func BenchmarkPAC(b *testing.B) {
	l := testLayout(b, false)
	for _, n := range []int{50, 1000, 10000} {
		frames := make([][]byte, n)
		for i := range frames {
			frames[i] = testFrame(b, l, time.UnixMilli(int64(i)), uint32(i), false)
		}
		for _, prefix := range []bool{false, true} {
			label := "plain"
			if prefix {
				label = "prefix"
			}
			b.Run(fmtBenchmarkName(n, label), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, e := PackPAC(l, frames, nil, prefix); e != nil {
						b.Fatal(e)
					}
				}
			})
		}
	}
}
func fmtBenchmarkName(n int, label string) string {
	if n == 50 {
		return "50/" + label
	}
	if n == 1000 {
		return "1000/" + label
	}
	return "10000/" + label
}

func TestPACEmptySingleAndCommonNull(t *testing.T) {
	l := testLayout(t, false)
	for _, frames := range [][][]byte{nil, {testFrame(t, l, time.UnixMilli(1), 1, false)}} {
		p, err := PackPAC(l, frames, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Common) != 0 {
			t.Fatal("empty/single PAC factored")
		}
		parsed, err := ParsePAC(l, p.Bytes, nil)
		if err != nil || len(parsed.Members) != len(frames) {
			t.Fatal(err)
		}
	}
	var frames [][]byte
	for i := 0; i < 4; i++ {
		var f Frame
		l.Scan(testFrame(t, l, time.UnixMilli(int64(i)), uint32(i), false), &f)
		f.Values[5] = Value{State: Null}
		raw, err := l.Encode(nil, f.Values)
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, raw)
	}
	p, err := PackPAC(l, frames, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	var prefix Frame
	if err = l.scanPlain(p.Common, &prefix); err != nil {
		t.Fatal(err)
	}
	if prefix.Values[5].State != Null {
		t.Fatal("common NULL lost")
	}
	for i, m := range p.Members {
		full, err := ResolvePAC(l, p.Common, m)
		if err != nil || !bytes.Equal(full, frames[i]) {
			t.Fatal("common NULL changed", err)
		}
	}
	// PAC parses structural PIL, not the application's per-event envelope marker.
	values := make([]Value, len(l.Widths))
	values[l.Event] = Value{State: Null}
	raw, err := l.Encode(nil, values)
	if err != nil {
		t.Fatal(err)
	}
	p, err = PackPAC(l, [][]byte{raw}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ParsePAC(l, p.Bytes, nil); err != nil {
		t.Fatal("PIL NULL confused with transport envelope", err)
	}
}
