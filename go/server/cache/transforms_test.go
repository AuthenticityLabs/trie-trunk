package cache

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIngressIdentityAndExplicitTransforms(t *testing.T) {
	d := testLayout(t, false).Definition
	d.Fields = append(d.Fields, Field{Name: "@fields", Type: "OBJECT"})
	l, e := Compile(d)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	cfg := Config{Root: t.TempDir(), Cache: "test", Tenant: "test", Layout: l, Now: func() time.Time { return now }}
	s, e := Open(cfg)
	if e != nil {
		t.Fatal(e)
	}
	dir := s.dir
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	config := fmt.Sprintf("target = %q\n[[steps]]\nop = 'rename'\nfrom = 'raw_message'\nto = 'message'\n", fmtDigestNative(l.Digest))
	if e = os.WriteFile(filepath.Join(dir, "transforms.cfg"), []byte(config), 0600); e != nil {
		t.Fatal(e)
	}
	s, e = Open(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { s.Close() }()
	ext, _ := encoder.Marshal(map[string]any{"raw_message": "explicitly transformed", "unmapped": true})
	values := make([]Value, len(d.Fields))
	values[len(values)-1] = Value{Offset, ext}
	raw, e := l.Encode(nil, values)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Append(Write, raw); e != nil {
		t.Fatal(e)
	}
	pending := s.branches[Write].pendingAll()
	if len(pending) != 1 {
		t.Fatal("not staged")
	}
	id := pending[0].id
	if id[6]>>4 != 7 || id[8]>>6 != 2 || eventTime(id[:]) != now.UnixMilli() {
		t.Fatal("missing UUID not generated correctly")
	}
	var frame Frame
	if e = l.Scan(pending[0].frame, &frame); e != nil {
		t.Fatal(e)
	}
	if string(frame.Values[5].Bytes) != "explicitly transformed" {
		t.Fatal("transform missing")
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(cfg)
	if e != nil {
		t.Fatal(e)
	}
	if s.branches[Write].pendingAll()[0].id != id {
		t.Fatal("UUID changed during recovery")
	}
	values[0] = uuid(now.UnixMilli(), 2)
	values[0].Bytes[6] = 0x40
	bad, _ := l.Encode(nil, values)
	if _, e = s.Append(Write, bad); e == nil {
		t.Fatal("UUIDv4 accepted")
	}
	ext, _ = encoder.Marshal(map[string]any{"raw_message": uint64(9)})
	values[0] = uuid(now.UnixMilli(), 3)
	values[len(values)-1] = Value{Offset, ext}
	wrong, _ := l.Encode(nil, values)
	if _, e = s.Append(Write, wrong); e == nil {
		t.Fatal("wrong declared type hidden in extensions")
	}
	var lost []Rejection
	if e = s.Rejections(func(r Rejection) error { lost = append(lost, r); return nil }); e != nil {
		t.Fatal(e)
	}
	if len(lost) != 2 || !bytes.Equal(lost[0].PIL, bad) || !bytes.Equal(lost[1].PIL, wrong) {
		t.Fatal("original lost inputs not preserved")
	}
}
