package cache

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	pil "github.com/AuthenticityLabs/pill-packer/go/codec"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func nativeControl(types ...string) *Layout {
	l := &Layout{Layout: &pil.Layout{Identity: pil.IdentityProfile{Primary: -1}}}
	for i, kind := range types {
		w, _ := width(kind)
		l.Widths = append(l.Widths, w)
		l.Definition.Fields = append(l.Definition.Fields, Field{Name: fmt.Sprint(i), Type: kind})
	}
	l.Layout.Definition.Fields = l.Definition.Fields
	return l
}
func readNativeTest(t *testing.T, raw []byte) ([]Manifest, []NativeChunk) {
	t.Helper()
	if bytes.HasPrefix(raw, []byte{31, 139}) {
		z, e := gzip.NewReader(bytes.NewReader(raw))
		if e != nil {
			t.Fatal(e)
		}
		raw, e = io.ReadAll(z)
		z.Close()
		if e != nil {
			t.Fatal(e)
		}
	}
	if !bytes.HasPrefix(raw, []byte{'T', 'P', 'I', 'L', 2}) {
		t.Fatal("preamble")
	}
	r := bytes.NewReader(raw[5:])
	var manifests []Manifest
	var chunks []NativeChunk
	open := false
	var rangeLeft uint64
	var e error
	for {
		tag := byte(7)
		if rangeLeft == 0 {
			var e error
			tag, e = r.ReadByte()
			if e != nil {
				t.Fatal(e)
			}
		}
		if tag == 9 {
			var h [12]byte
			if _, e = io.ReadFull(r, h[:]); e != nil {
				t.Fatal(e)
			}
			rangeLeft = order.Uint64(h[4:])
			if !open || rangeLeft == 0 || rangeLeft > uint64(r.Len()) {
				t.Fatal("invalid PAC range")
			}
			continue
		}
		if tag == 255 {
			last, _ := r.ReadByte()
			if last != 254 || r.Len() != 0 || open {
				t.Fatal("incomplete native stream")
			}
			break
		}
		if tag == 7 {
			h := make([]byte, 4)
			if _, e = io.ReadFull(r, h); e != nil {
				t.Fatal(e)
			}
			n := uint64(order.Uint32(h[:4]))
			if n < PACCompressedHeaderSize || n > PACMaximum {
				t.Fatal("PAC length")
			}
			data := make([]byte, n)
			copy(data, h)
			if _, e = io.ReadFull(r, data[4:]); e != nil {
				t.Fatal(e)
			}
			if rangeLeft > 0 {
				if n > rangeLeft {
					t.Fatal("PAC exceeds range")
				}
				rangeLeft -= n
			}
			chunks = append(chunks, NativeChunk{Kind: "pac", Data: data})
			continue
		}
		var l *Layout
		switch tag {
		case 1:
			l = nativeControl("BYTES", "BYTES", "BYTES")
		case 2:
			l = nativeControl("U32", "BYTES", "STRING")
		case 4:
			l = nativeControl("U32", "OBJECT")
		case 5:
			l = nativeControl("U32", "U8", "U64", "BYTES", "BYTES")
		case 6:
			l = nativeControl("U32")
		default:
			t.Fatal("unknown tag", tag)
		}
		b, e := l.ReadFrame(r, nil)
		if e != nil {
			t.Fatal(e)
		}
		var f Frame
		if e = l.Scan(b, &f); e != nil {
			t.Fatal(tag, e)
		}
		switch tag {
		case 4:
			if open {
				t.Fatal("overlapping packages")
			}
			open = true
			var m Manifest
			if e = decoder.Unmarshal(f.Values[1].Bytes, &m); e != nil {
				t.Fatal(e)
			}
			manifests = append(manifests, m)
		case 5:
			if !open {
				t.Fatal("chunk outside package")
			}
			kinds := []string{"map", "trie", "state", "index"}
			kind := int(f.Values[1].Bytes[0])
			if kind >= len(kinds) {
				t.Fatal("kind")
			}
			var digest [32]byte
			copy(digest[:], f.Values[3].Bytes)
			data := f.Values[4].Bytes
			if sha256.Sum256(data) != digest {
				t.Fatal("chunk checksum")
			}
			chunks = append(chunks, NativeChunk{Kind: kinds[kind], Start: unsigned(f.Values[2].Bytes), Data: append([]byte(nil), data...), Digest: digest})
		case 6:
			if !open {
				t.Fatal("unexpected completion")
			}
			open = false
		}
	}
	return manifests, chunks
}

func TestNativeCollectHTTPGapOnlyAndLazyPayload(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	p, e := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(2 * time.Hour) }})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	s := p.api.Store
	for i := 0; i < 6; i++ {
		if _, e = s.Append(Write, testFrame(t, l, at.Add(time.Duration(i)*10*time.Minute), uint32(i+1), false)); e != nil {
			t.Fatal(e)
		}
	}
	from, to := at.Add(12*time.Minute), at.Add(28*time.Minute)
	path := "/collect?format=native&tap=" + url.QueryEscape(p.api.tap(Write)) + "&from=" + url.QueryEscape(from.Format(time.RFC3339Nano)) + "&to=" + url.QueryEscape(to.Format(time.RFC3339Nano))
	request := func(have *Coverage, capability bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("PIL-Version", "2")
		r.Header.Set("Accept", "application/pil")
		r.Header.Set("Accept-Encoding", "gzip")
		if capability {
			r.Header.Set("PIL-Features", "native-ranges,adaptive-integers")
		}
		if have != nil {
			raw, _ := json.Marshal([]Coverage{*have})
			r.Header.Set("Trunk-Coverage", base64.RawURLEncoding.EncodeToString(raw))
			r.Header.Set("Definition-Known", fmtDigest(l))
		}
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		return w
	}
	if w := request(nil, false); w.Code != 426 {
		t.Fatal("unnegotiated extension", w.Code)
	}
	cold := request(nil, true)
	if cold.Code != 200 {
		t.Fatal(cold.Code, cold.Body.String())
	}
	ms, chunks := readNativeTest(t, cold.Body.Bytes())
	if len(ms) != 1 || ms[0].Provisional || len(ms[0].Index) != 1 || ms[0].Index[0] != (Span{1, 7}) {
		t.Fatal("not complete hourly index", ms)
	}
	var idx, pods []byte
	for _, c := range chunks {
		if c.Kind == "index" {
			idx = append(idx, c.Data...)
		}
		if c.Kind == "pac" {
			pods = append(pods, c.Data...)
		}
	}
	if !bytes.Equal(idx, s.branches[Write].rows[IndexRowSize:]) {
		t.Fatal("index was reencoded")
	}
	lo, hi, e := s.branches[Write].podsRange(2, s.row)
	if e != nil {
		t.Fatal(e)
	}
	source, e := readBytes(s.branches[Write].pods, lo, hi)
	if e != nil || !bytes.Equal(source, pods) {
		t.Fatal("not exact lazy payload", e)
	}
	m := ms[0]
	have := Coverage{Definition: m.Definition, Generation: m.Generation, Index: m.Index, MemberRanges: m.MemberRanges, TrieNodes: m.TrieNodes}
	warm := request(&have, true)
	_, chunks = readNativeTest(t, warm.Body.Bytes())
	for _, c := range chunks {
		if c.Kind != "state" {
			t.Fatal("redundant transfer", c.Kind)
		}
	}
	have.Index = []Span{{1, 3}, {4, 7}}
	warm = request(&have, true)
	_, chunks = readNativeTest(t, warm.Body.Bytes())
	for _, c := range chunks {
		if c.Kind == "index" && (c.Start != 1 || len(c.Data) != 6*IndexRowSize) {
			t.Fatal("wrong gap", c)
		}
	}
	if dir := os.Getenv("PIL_NATIVE_RANGE_FIXTURE_DIR"); dir != "" {
		if e = os.MkdirAll(dir, 0755); e != nil {
			t.Fatal(e)
		}
		for name, raw := range map[string][]byte{"ranges.pil": ungzipTest(t, cold.Body.Bytes()), "definition.rx": l.Bytes, "index.bin": idx, "pods.bin": pods} {
			if e = os.WriteFile(filepath.Join(dir, name), raw, 0644); e != nil {
				t.Fatal(e)
			}
		}
	}
	// Completion lives in the response body; an interrupted body is never enough
	// to publish coverage even if all payload chunks happened to arrive.
}

func ungzipTest(t testing.TB, raw []byte) []byte {
	t.Helper()
	if bytes.HasPrefix(raw, []byte("TPIL")) {
		return raw
	}
	z, e := gzip.NewReader(bytes.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	out, e := io.ReadAll(z)
	z.Close()
	if e != nil {
		t.Fatal(e)
	}
	return out
}
