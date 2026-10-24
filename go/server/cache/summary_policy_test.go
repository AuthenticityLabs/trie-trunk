package cache

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSummaryCompressionPolicy(t *testing.T) {
	if (Policy{}).Summary.level() != 6 {
		t.Fatal("daily summaries should default to measured balanced gzip")
	}
	for _, level := range []int{0, 1, 6, 9, 10} {
		c, e := ParseConfiguration([]byte(fmt.Sprintf("version=1\ntimezone='UTC'\n[defaults.summary]\ngzip_level=%d\n", level)))
		if level < 1 || level > 9 {
			if e == nil {
				t.Fatal("accepted invalid level", level)
			}
			continue
		}
		if e != nil || c.Resolve("payment/transactions.v1").Summary.level() != level {
			t.Fatal(level, e)
		}
	}
	c, e := ParseConfiguration([]byte("version=1\ntimezone='UTC'\n[defaults.summary]\ngzip_level=9\n[resources.'payment/transactions.v1'.summary]\ngzip_level=6\n"))
	if e != nil || c.Resolve("payment/transactions.v1").Summary.level() != 6 || c.Resolve("other/items.v1").Summary.level() != 9 {
		t.Fatal("inheritance", e)
	}
	for _, inherit := range []bool{true, false} {
		p := filepath.Join(t.TempDir(), "resource.cfg")
		if e = os.WriteFile(p, []byte(fmt.Sprintf("version=1\ninherit=%t\n", inherit)), 0600); e != nil {
			t.Fatal(e)
		}
		policy, _, e := readScope(p, c.Defaults, true)
		want := 6
		if inherit {
			want = 9
		}
		if e != nil || policy.Summary.level() != want {
			t.Fatal("scope", e)
		}
	}
	sum := aggregateFixture()
	for _, level := range []int{1, 6, 9} {
		raw, e := encodeSummaryDayLevel(0, 0, summaryDayMS, []Summary{sum}, true, level)
		if e != nil {
			t.Fatal(e)
		}
		_, _, _, got, e := decodeSummaryDay(raw)
		if e != nil || !reflect.DeepEqual(got, []Summary{sum}) {
			t.Fatal("level roundtrip", level, e)
		}
		plain, e := encodeSummaryDayFiltered(0, 0, summaryDayMS, []Summary{sum}, false, level, true, true)
		if e != nil {
			t.Fatal(e)
		}
		var out bytes.Buffer
		z, _ := gzip.NewWriterLevel(&out, level)
		z.Write(plain[32:])
		z.Close()
		if !bytes.Equal(out.Bytes(), raw[32:]) {
			t.Fatal("encoder ignored level", level)
		}
	}
}

// Uses persisted packets from the real 50k-event fixture, not invented text.
func BenchmarkSavedSummaryCompression(b *testing.B) {
	root := os.Getenv("TRUNK_SUMMARY_SAMPLES")
	if root == "" {
		b.Skip("set TRUNK_SUMMARY_SAMPLES")
	}
	for i := 1; i <= 3; i++ {
		raw, e := os.ReadFile(filepath.Join(root, fmt.Sprintf("sample-%d.sumday", i)))
		if e != nil {
			b.Fatal(e)
		}
		z, e := gzip.NewReader(bytes.NewReader(raw[32:]))
		if e != nil {
			b.Fatal(e)
		}
		body, e := io.ReadAll(z)
		z.Close()
		if e != nil {
			b.Fatal(e)
		}
		for _, level := range []int{6, 9} {
			b.Run(fmt.Sprintf("sample%d/gzip%d", i, level), func(b *testing.B) {
				b.ReportAllocs()
				for n := 0; n < b.N; n++ {
					var out bytes.Buffer
					w, _ := gzip.NewWriterLevel(&out, level)
					w.Write(body)
					w.Close()
					b.ReportMetric(float64(out.Len()+32), "bytes/packet")
				}
			})
		}
	}
}
