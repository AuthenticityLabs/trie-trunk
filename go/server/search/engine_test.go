package search

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
	"trie-trunk/go/codec"
)

func TestResidentReaderReuseAndCancellation(t *testing.T) {
	c, _, _, now := fixture(t)
	d := testDefinition()
	d.Storage = &codec.StoragePolicy{Format: 3, Boundary: "missing", Bucket: "hour"}
	rev, data, e := d.Prepare()
	if e != nil {
		t.Fatal(e)
	}
	m := mustOpen(t, c, rev, data)
	defer m.Close()
	appendSearchRecord(t, m, rev, now.Add(-2*time.Hour), codec.Record{14: uint64(401)})
	appendSearchRecord(t, m, rev, now.Add(-time.Hour), codec.Record{14: uint64(200)})
	if e = m.Flush(); e != nil {
		t.Fatal(e)
	}
	engine := NewEngine(c.Root, 1)
	defer engine.Close()
	q := Config{Root: c.Root, Start: now.Add(-3 * time.Hour), End: now.Add(time.Second)}
	var pid int
	for _, where := range [][]string{nil, {"http_status==401"}, {"http_status==200"}, nil} {
		q.Where = where
		var want, got bytes.Buffer
		if e = Run(context.Background(), q, &want); e != nil {
			t.Fatal(e)
		}
		if e = engine.Run(context.Background(), q, &got); e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(want.Bytes(), got.Bytes()) {
			t.Fatal("resident output differs")
		}
		if pid != 0 && pid != engine.cmd.Process.Pid {
			t.Fatal("worker restarted")
		}
		pid = engine.cmd.Process.Pid
	}
	q.Follow = true
	q.End = time.UnixMilli(1 << 48)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if e = engine.Run(ctx, q, io.Discard); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	q.Follow = false
	q.End = now.Add(time.Second)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	if e = engine.Run(ctx2, q, io.Discard); e != nil {
		t.Fatal("canceled follower retained worker", e)
	}
}
