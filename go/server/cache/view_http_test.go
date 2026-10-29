package cache

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	pil "github.com/AuthenticityLabs/pill-packer/go/codec"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type viewDecoded struct {
	result      ViewResult
	definitions int
	frames      [][]byte
	bindings    map[uint32]string
}

func decodeView(t *testing.T, l *Layout, raw []byte) (d viewDecoded) {
	t.Helper()
	d.bindings = map[uint32]string{}
	if !bytes.HasPrefix(raw, []byte{'T', 'P', 'I', 'L', 2}) {
		t.Fatalf("invalid response: %q", raw)
	}
	r := bytes.NewReader(raw[5:])
	control := &pil.Layout{Widths: []int{0}, Definition: pil.Definition{Fields: []Field{{Name: "metadata", Type: "BYTES"}}}}
	for {
		tag, e := r.ReadByte()
		if e != nil {
			t.Fatal(e)
		}
		switch tag {
		case 1:
			d.definitions++
			_, e = (&pil.Layout{Widths: []int{0, 0, 0}}).ReadFrame(r, nil)
		case 2:
			cl := nativeControl("U32", "BYTES", "STRING")
			var raw []byte
			raw, e = cl.ReadFrame(r, nil)
			if e == nil {
				var f Frame
				e = cl.Scan(raw, &f)
				if e == nil {
					d.bindings[uint32(unsigned(f.Values[0].Bytes))] = string(f.Values[2].Bytes)
				}
			}
		case 8:
			var f []byte
			f, e = control.ReadFrame(r, nil)
			if e == nil {
				var v Frame
				e = control.Scan(f, &v)
				if e == nil {
					e = json.Unmarshal(v.Values[0].Bytes, &d.result)
				}
			}
		case 7:
			var header [PACHeaderSize]byte
			if _, err := io.ReadFull(r, header[:]); err != nil {
				t.Fatal(err)
			}
			size := order.Uint64(header[8:16])
			if size < PACHeaderSize || size > PACMaximum {
				t.Fatal("PAC size")
			}
			raw := make([]byte, size)
			copy(raw, header[:])
			if _, err := io.ReadFull(r, raw[PACHeaderSize:]); err != nil {
				t.Fatal(err)
			}
			pac, err := ParsePAC(l, raw, map[uint32]bool{PACSource: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, member := range pac.Members {
				full, err := ResolvePAC(l, pac.Common, member)
				if err != nil {
					t.Fatal(err)
				}
				d.frames = append(d.frames, full)
			}

		case 255:
			done, _ := r.ReadByte()
			if done != 254 || r.Len() != 0 {
				t.Fatal("incomplete response")
			}
			return
		default:
			t.Fatalf("unknown tag %d", tag)
		}
		if e != nil {
			t.Fatal(e)
		}
	}
}
func readViewEUI(r io.Reader) (uint64, error) {
	var b [8]byte
	if _, e := io.ReadFull(r, b[:2]); e != nil {
		return 0, e
	}
	n := uint64(order.Uint16(b[:2]))
	if n < 65535 {
		return n, nil
	}
	if _, e := io.ReadFull(r, b[:4]); e != nil {
		return 0, e
	}
	n = uint64(order.Uint32(b[:4]))
	if n < 0xffffffff {
		return n, nil
	}
	_, e := io.ReadFull(r, b[:])
	return order.Uint64(b[:]), e
}
func TestPagedViewNegotiationSnapshotAndCache(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	now := at.Add(time.Hour)
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	originals := [][]byte{}
	for i := 0; i < 7; i++ {
		f := testFrame(t, l, at.Add(time.Duration(i)*time.Minute), uint32(i+1), false)
		originals = append(originals, f)
		if _, e = s.Append(Write, f); e != nil {
			t.Fatal(e)
		}
	}
	h := &HTTP{Store: s, Token: "test"}
	path := "/events?tap=" + url.QueryEscape(h.tap(Write)) + "&from=2026-09-01T12:00:00Z&to=2026-09-01T14:00:00Z&page=2"
	get := func(path string, headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer test")
		r.Header.Set("Accept", "application/pil")
		r.Header.Set("PIL-Version", "2")
		r.Header.Set("PIL-Features", "pac-arrays,adaptive-integers")
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	first := get(path, nil)
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	d := collectView(t, l, h, first.Body.Bytes())
	if d.definitions != 1 || len(d.frames) != 2 || d.result.Summary.Count != 7 || d.result.Next == "" {
		t.Fatalf("first %#v", d)
	}
	for i, f := range d.frames {
		if !sameSeedData(t, l, f, originals[i]) {
			t.Fatal("changed frame")
		}
	}
	compressed := get(path, map[string]string{"Accept-Encoding": "gzip"})
	if compressed.Code != 200 || compressed.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("missing whole-response gzip")
	}
	gz, err := gzip.NewReader(bytes.NewReader(compressed.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(gz)
	gz.Close()
	if err != nil {
		t.Fatal(err)
	}
	z := collectView(t, l, h, decoded)
	if len(z.frames) != len(d.frames) || z.result.Summary.Count != d.result.Summary.Count {
		t.Fatal("gzip response differs")
	}
	for i := range z.frames {
		if !bytes.Equal(z.frames[i], d.frames[i]) {
			t.Fatal("gzip changed frame")
		}
	}
	if no := get(path, map[string]string{"Accept-Encoding": "gzip;q=0"}); no.Header().Get("Content-Encoding") != "" {
		t.Fatal("ignored gzip rejection")
	}
	known := []string{}
	for _, r := range d.result.Page {
		known = append(known, r.Key)
	}
	warm := get(path, map[string]string{"Definition-Known": fmtDigest(l), "Summary-Known": d.result.SummaryVersion, "Event-Known": strings.Join(known, ",")})
	warmCompressed := get(path, map[string]string{"Accept-Encoding": "gzip", "Definition-Known": fmtDigest(l), "Summary-Known": d.result.SummaryVersion, "Event-Known": strings.Join(known, ",")})
	w := collectView(t, l, h, warm.Body.Bytes())
	if w.definitions != 0 || len(w.frames) != 0 || w.result.Summary != nil || len(w.result.Page) != 2 || !w.result.Page[0].Cached {
		t.Fatal("cache negotiation failed")
	}
	// A newly finalized event must not enter an existing cursor's tail or summary.
	if _, e = s.Append(Write, testFrame(t, l, at.Add(50*time.Minute), 99, false)); e != nil {
		t.Fatal(e)
	}
	now = now.Add(time.Hour)
	seen := 2
	var nextFixture, nextCompressed []byte
	cursor := d.result.Next
	for cursor != "" {
		reply := get(path+"&after="+url.QueryEscape(cursor), map[string]string{"Summary-Known": d.result.SummaryVersion})
		if reply.Code != 200 {
			t.Fatal(reply.Code, reply.Body.String())
		}
		if nextFixture == nil {
			nextFixture = append([]byte(nil), reply.Body.Bytes()...)
			nextCompressed = get(path+"&after="+url.QueryEscape(cursor), map[string]string{"Accept-Encoding": "gzip", "Summary-Known": d.result.SummaryVersion}).Body.Bytes()
		}
		p := collectView(t, l, h, reply.Body.Bytes())
		if p.result.Snapshot != d.result.Snapshot || p.result.Summary != nil {
			t.Fatal("snapshot drift")
		}
		seen += len(p.frames)
		cursor = p.result.Next
	}
	if seen != 7 {
		t.Fatal("page lost or duplicated events", seen)
	}
	previous := collectView(t, l, h, get(path+"&after="+url.QueryEscape(d.result.Cursor), nil).Body.Bytes())
	if previous.result.Snapshot != d.result.Snapshot || previous.result.Summary.Count != 7 {
		t.Fatal("previous page lost snapshot")
	}
	fresh := collectView(t, l, h, get(path, nil).Body.Bytes())
	if fresh.result.Summary.Count != 8 {
		t.Fatal("refresh missed new event")
	}
	bad := get(path+"&after="+url.QueryEscape(d.result.Next+"x"), nil)
	if bad.Code != 409 {
		t.Fatal("tampered cursor", bad.Code)
	}
	changed := get(strings.Replace(path, "14:00:00", "15:00:00", 1)+"&after="+url.QueryEscape(d.result.Next), nil)
	if changed.Code != 409 {
		t.Fatal("changed query", changed.Code)
	}
	unauthorized := get(path, map[string]string{"Authorization": "Bearer wrong"})
	if unauthorized.Code != 401 {
		t.Fatal("authorization", unauthorized.Code)
	}
	// Summary-only requests don't read .pods values (damage a frame after commit).
	b := s.branches[Write]
	if _, e = b.pods.WriteAt([]byte{255, 255, 255, 255}, headerSize); e != nil {
		t.Fatal(e)
	}
	summary := get(path+"&include=summary", nil)
	if summary.Code != 200 {
		t.Fatal(summary.Body.String())
	}
	only := collectView(t, l, h, summary.Body.Bytes())
	if len(only.frames) != 0 || only.result.Summary.Count != 8 {
		t.Fatal("summary hydrated payload")
	}
	if dir := os.Getenv("PIL_VIEW_FIXTURE_DIR"); dir != "" {
		if e = os.MkdirAll(dir, 0755); e != nil {
			t.Fatal(e)
		}
		for name, raw := range map[string][]byte{"cold.pil.gz": compressed.Body.Bytes(), "warm.pil.gz": warmCompressed.Body.Bytes(), "next.pil.gz": nextCompressed, "next.pil": nextFixture, "cold.pil": first.Body.Bytes(), "warm.pil": warm.Body.Bytes(), "definition.rx": l.Bytes} {
			if e = os.WriteFile(filepath.Join(dir, name), raw, 0644); e != nil {
				t.Fatal(e)
			}
		}
	}
}
func TestPagedViewCursorIsolationAndExpiry(t *testing.T) {
	c := viewCursor{Expires: time.Now().Add(time.Minute).Unix()}
	raw, e := viewSign(c, "a")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = viewParse(raw, "b"); e == nil {
		t.Fatal("cross credential cursor")
	}
	c.Expires = time.Now().Add(-time.Minute).Unix()
	raw, _ = viewSign(c, "a")
	if _, e = viewParse(raw, "a"); e == nil {
		t.Fatal("expired cursor")
	}
}
func TestPACArrayExactFraming(t *testing.T) {
	l := testLayout(t, false)
	frames := [][]byte{testFrame(t, l, time.UnixMilli(1), 1, false), testFrame(t, l, time.UnixMilli(2), 2, false)}
	var b bytes.Buffer
	if err := writePAC(&b, l, "demo/payment/transactions.v1/write", frames); err != nil {
		t.Fatal(err)
	}
	if b.Bytes()[0] != 7 {
		t.Fatal("PAC stream tag")
	}
	p, err := ParsePAC(l, b.Bytes()[1:], map[uint32]bool{PACSource: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Common) == 0 || len(p.Members) != 2 {
		t.Fatal("prefix packing missing")
	}
	for i, m := range p.Members {
		full, err := ResolvePAC(l, p.Common, m)
		if err != nil || !bytes.Equal(full, frames[i]) {
			t.Fatal("round trip", err)
		}
	}
}

func TestPagedViewBranchesBoundaryAndGeneration(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(time.Hour) }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	// Repeated UUIDs across branches require a source/row tie-breaker.
	for _, b := range []Branch{Write, Error} {
		for n := 0; n < 3; n++ {
			if _, e = s.Append(b, testFrame(t, l, at.Add(time.Duration(n)*time.Millisecond), uint32(n), false)); e != nil {
				t.Fatal(e)
			}
		}
	}
	h := &HTTP{Store: s}
	path := "/events?from=2026-09-01T12:00:00.0005Z&to=2026-09-01T12:00:00.0025Z&page=1"
	get := func(p string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", p, nil)
		r.Header.Set("Accept", "application/pil")
		r.Header.Set("PIL-Version", "2")
		r.Header.Set("PIL-Features", "pac-arrays")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	first := get(path)
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	d := collectView(t, l, h, first.Body.Bytes())
	if d.result.Summary.Count != 4 || d.result.Summary.Errors[0] != 2 {
		t.Fatalf("fractional time bounds %#v", d.result.Summary)
	}
	seen := map[string]bool{}
	var previous []byte
	next := ""
	for {
		p := path
		if next != "" {
			p += "&after=" + url.QueryEscape(next)
		}
		w := get(p)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		d := collectView(t, l, h, w.Body.Bytes())
		if len(d.frames) != 1 {
			t.Fatal("page size")
		}
		var f Frame
		if e = l.Scan(d.frames[0], &f); e != nil {
			t.Fatal(e)
		}
		id := f.Values[l.Event].Bytes
		if bytes.Compare(previous, id) > 0 {
			t.Fatal("global ordering")
		}
		previous = append(previous[:0], id...)
		key := d.result.Page[0].Key
		if seen[key] {
			t.Fatal("repeated reference")
		}
		seen[key] = true
		next = d.result.Next
		if next == "" {
			break
		}
	}
	if len(seen) != 4 {
		t.Fatal("lost equal-timestamp events")
	}
	// A physical rewrite cannot silently reinterpret a cursor as a new generation.
	s.branches[Write].generation[0] ^= 1
	if w := get(path + "&after=" + url.QueryEscape(d.result.Next)); w.Code != 409 {
		t.Fatal("generation change", w.Code)
	}
}
