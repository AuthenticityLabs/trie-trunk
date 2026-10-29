package cache

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestNativeSnapshotDoesNotBlockWriterOrRetention(t *testing.T) {
	for _, mode := range []string{"all", "payload-only", "fully-covered"} {
		t.Run(mode, func(t *testing.T) { testNativeSnapshot(t, mode) })
	}
}
func testNativeSnapshot(t *testing.T, mode string) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	now := at.Add(2 * time.Hour)
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	s.policy.Retention.Write = "1h"
	if _, e = s.Append(Write, testFrame(t, l, at, 1, false)); e != nil {
		t.Fatal(e)
	}
	request := CollectionRequest{Branch: Write, At: at, DataFrom: at, DataTo: at.Add(time.Hour)}
	if mode != "all" {
		var have Manifest
		if e = s.Collect(context.Background(), request, func(m Manifest) error { have = m; return nil }, func(NativeChunk) error { return nil }); e != nil {
			t.Fatal(e)
		}
		request.Have = Coverage{Definition: have.Definition, Generation: have.Generation, Index: have.Index, TrieNodes: have.TrieNodes}
		if mode == "fully-covered" {
			request.Have.MemberRanges = have.MemberRanges
		}
	}
	var expected []NativeChunk
	if e = s.Collect(context.Background(), request, func(Manifest) error { return nil }, func(c NativeChunk) error {
		c.Data = append([]byte(nil), c.Data...)
		expected = append(expected, c)
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	ready, release := make(chan Manifest, 1), make(chan struct{})
	done := make(chan error, 1)
	var got []NativeChunk
	go func() {
		done <- s.Collect(context.Background(), request, func(m Manifest) error { ready <- m; <-release; return nil }, func(c NativeChunk) error {
			c.Data = append([]byte(nil), c.Data...)
			got = append(got, c)
			return nil
		})
	}()
	old := <-ready
	writes := make(chan error, 1)
	go func() {
		_, err := s.Append(Write, testFrame(t, l, now, 2, false))
		if err == nil {
			err = s.ApplyRetention()
		}
		writes <- err
	}()
	select {
	case e = <-writes:
		if e != nil {
			close(release)
			<-done
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		close(release)
		<-done
		t.Fatal("native network callback held writer/retention lock")
	}
	close(release)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if len(got) != len(expected) {
		t.Fatal("snapshot chunk count", len(got), len(expected))
	}
	for i, c := range got {
		if c.Kind != expected[i].Kind || c.Start != expected[i].Start || !bytes.Equal(c.Data, expected[i].Data) {
			t.Fatalf("snapshot changed at chunk %d", i)
		}
	}
	if old.Generation != s.branches[Write].generation {
		t.Fatal("retention changed source identity")
	}
	if old.GenerationSequence != 1 {
		t.Fatal("snapshot generation sequence changed", old.GenerationSequence)
	}
	if e = s.Collect(context.Background(), request, func(m Manifest) error {
		if m.GenerationSequence != 1 || m.Generation != old.Generation || m.RetiredRows <= old.RetiredRows {
			t.Fatal("new snapshot did not expose retirement boundary", m)
		}
		return nil
	}, func(NativeChunk) error { return nil }); e != nil {
		t.Fatal(e)
	}
	if len(s.branches[Write].rows) != 0 {
		t.Fatal("expired rows survived retention")
	}
}
