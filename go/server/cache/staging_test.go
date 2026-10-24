package cache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestStagingHoldbackLiveSearchAndLateAudit(t *testing.T) {
	l := testLayout(t, false)
	now := time.Date(2026, 10, 17, 12, 15, 0, 0, time.UTC)
	at := now.Add(-time.Minute)
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }, BatchRecords: 1}
	s, e := Open(c)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { s.Close() }()
	first := testFrame(t, l, at, 1, false)
	earlier := testFrame(t, l, at.Add(-time.Second), 2, true)
	for _, raw := range [][]byte{first, earlier} {
		if _, e = s.Append(Write, raw); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	b := s.branches[Write]
	if len(b.rows) != 0 || b.popEnd != headerSize {
		t.Fatal("young records finalized")
	}
	q := Query{Branch: Write, From: at.Add(-time.Hour), To: now}
	var live [][]byte
	if e = s.SearchLive(context.Background(), q, func(raw []byte) error { live = append(live, append([]byte(nil), raw...)); return nil }); e != nil {
		t.Fatal(e)
	}
	if len(live) != 2 || !bytes.Equal(live[0], earlier) {
		t.Fatal("staged search missing/out of order")
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(c)
	if e != nil {
		t.Fatal(e)
	}
	if len(s.branches[Write].rows) != 0 || s.branches[Write].live.count(0) != 2 {
		t.Fatal("close bypassed holdback or recovery lost events")
	}
	now = now.Add(80 * time.Minute)
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	got := collect(t, s, Write, q.From, now)
	if len(got) != 2 || !bytes.Equal(got[0], earlier) || !bytes.Equal(got[1], first) {
		t.Fatal("final storage not sorted")
	}
	if dup, e := s.Append(Write, first); e != nil || !dup {
		t.Fatal("retry", dup, e)
	}
	rejected := testFrame(t, l, at.Add(-2*time.Second), 3, false)
	_, e = s.Append(Write, rejected)
	var late *LateEventError
	if !errors.As(e, &late) {
		t.Fatalf("expected rejection: %v", e)
	}
	n := 0
	if e = s.Rejections(func(r Rejection) error {
		n++
		if !bytes.Equal(r.PIL, rejected) || r.Reason != "arrival_after_finalization" {
			t.Fatal("lost audit payload")
		}
		return nil
	}); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	// The empty closed interval and audit survive restart too.
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(c)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Append(Write, testFrame(t, l, time.UnixMilli(s.branches[Write].finalizedBefore-1), 4, false))
	if !errors.As(e, &late) {
		t.Fatal("closed empty interval reopened", e)
	}
}

// A real SIGKILL, with no deferred Close, tests the acknowledged append boundary.
func TestStagingSIGKILLRecovery(t *testing.T) {
	root := os.Getenv("TRUNK_STAGE_KILL_ROOT")
	at := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	l := testLayout(t, false)
	if root != "" {
		s, e := Open(Config{Root: root, Cache: "demo", Tenant: "test", Layout: l, ObjectIndex: true, Now: func() time.Time { return at.Add(time.Minute) }})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.Append(Write, testFrame(t, l, at, 1, false)); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(root, "ready"), []byte("durable"), 0600); e != nil {
			t.Fatal(e)
		}
		select {}
	}
	root = t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestStagingSIGKILLRecovery$", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), "TRUNK_STAGE_KILL_ROOT="+root)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { cmd.Process.Kill() }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, e := os.Stat(filepath.Join(root, "ready")); e == nil {
			break
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatal("child startup", output.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e := cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	if e := cmd.Wait(); e == nil {
		t.Fatal("child not killed")
	}
	s, e := Open(Config{Root: root, Cache: "demo", Tenant: "test", Layout: l, ObjectIndex: true, Now: func() time.Time { return at.Add(time.Minute) }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if s.branches[Write].live.count(0) != 1 {
		t.Fatal("acknowledged event lost")
	}
	n := 0
	if e = s.History(context.Background(), testObject(42), at, at.Add(time.Hour), func(row uint64, raw []byte) error {
		n++
		if row != ProvisionalRow || !bytes.Equal(raw, testFrame(t, l, at, 1, false)) {
			t.Fatal("recovered live history")
		}
		return nil
	}); e != nil || n != 1 {
		t.Fatal("acknowledged history missing after kill", n, e)
	}
	if len(s.branches[Write].rows) != 0 {
		t.Fatal("recovery finalized young event")
	}
	if got := func() [][]byte {
		var frames [][]byte
		e = s.SearchLive(context.Background(), Query{Branch: Write, From: at, To: at.Add(time.Hour)}, func(raw []byte) error { frames = append(frames, append([]byte(nil), raw...)); return nil })
		if e != nil {
			t.Fatal(e)
		}
		return frames
	}(); len(got) != 1 || !bytes.Equal(got[0], testFrame(t, l, at, 1, false)) {
		t.Fatal("recovery payload")
	}
}

func TestStagingRecoveryAfterCommitBeforeCleanup(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(2 * time.Hour) }, BatchRecords: 1}
	s, e := Open(c)
	if e != nil {
		t.Fatal(e)
	}
	raw := testFrame(t, l, at, 1, false)
	if _, e = s.Append(Write, raw); e != nil {
		t.Fatal(e)
	}
	b := s.branches[Write]
	if _, e = s.Append(Write, testFrame(t, l, at, 2, true)); e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	e = s.commitBatch(b, b.pendingAll())
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(s.dir, ".seed-interrupted"), []byte("unfinished replacement"), 0600); e != nil {
		t.Fatal(e)
	}
	// Simulate death after the permanent commit, before atomic swap cleanup.
	if e = releaseCrashedTestStore(s); e != nil {
		t.Fatal(e)
	}
	s, e = Open(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if s.branches[Write].live.count(0) != 0 {
		t.Fatal("committed hour replayed")
	}
	if _, e = os.Stat(filepath.Join(s.dir, ".seed-interrupted")); !os.IsNotExist(e) {
		t.Fatal("orphan replacement not cleaned", e)
	}
	if got := collect(t, s, Write, at, at.Add(time.Hour)); len(got) != 2 {
		t.Fatal("duplicate after crash")
	}
}

func TestStagingTornTailAndCorruption(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			l := testLayout(t, false)
			at := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
			c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(time.Minute) }}
			s, e := Open(c)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.Append(Write, testFrame(t, l, at, 1, false)); e != nil {
				t.Fatal(e)
			}
			b := s.branches[Write]
			path := s.swapPath(b)
			if e = releaseCrashedTestStore(s); e != nil {
				t.Fatal(e)
			}
			f, e := os.OpenFile(path, os.O_RDWR, 0600)
			if e != nil {
				t.Fatal(e)
			}
			if corrupt {
				_, e = f.WriteAt([]byte{255}, headerSize+16+8)
			} else {
				_, e = f.Seek(0, 2)
				if e == nil {
					_, e = f.Write([]byte{1, 2, 3})
				}
			}
			f.Close()
			if e != nil {
				t.Fatal(e)
			}
			s, e = Open(c)
			if corrupt {
				if e == nil {
					s.Close()
					t.Fatal("accepted corrupt full record")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			if s.branches[Write].live.count(0) != 1 {
				t.Fatal("lost durable prefix")
			}
		})
	}
}

func TestStagingRetentionKeepsYoungTail(t *testing.T) {
	l := testLayout(t, false)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }}
	s, e := Open(c)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { s.Close() }()
	old := testFrame(t, l, now.Add(-48*time.Hour), 1, false)
	young := testFrame(t, l, now.Add(-time.Minute), 2, false)
	if _, e = s.AppendBatch(Write, [][]byte{young, old}); e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	e = s.prune(Write, now.Add(-24*time.Hour))
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	if len(s.branches[Write].rows) != 0 || s.branches[Write].live.count(0) != 1 {
		t.Fatal("retention lost young tail")
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(c)
	if e != nil {
		t.Fatal(e)
	}
	now = now.Add(time.Hour)
	got := collect(t, s, Write, now.Add(-2*time.Hour), now)
	if len(got) != 1 || !bytes.Equal(got[0], young) {
		t.Fatal("retention/restart lost pending event")
	}
}

func TestStagingIdlePromotionAndClockRollback(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	// Update the test clock under the same mutex as production maintenance.
	now := at.Add(time.Minute)
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = s.Append(Write, testFrame(t, l, at, 1, false)); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, 5*time.Millisecond) }()
	s.mu.Lock()
	now = at.Add(2 * time.Hour)
	s.mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		rows := len(s.branches[Write].rows)
		s.mu.Unlock()
		if rows == 2*s.row.Width {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("idle tail never promoted")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	cutoff := s.branches[Write].finalizedBefore
	now = at.Add(time.Minute)
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	if s.branches[Write].finalizedBefore != cutoff {
		t.Fatal("clock rollback reopened finalized time")
	}
}

// Simulate loss before finalization while terminating the newly added indexing
// workers too. Closing file descriptors alone is not equivalent to process death.
func releaseCrashedTestStore(s *Store) error {
	s.mu.Lock()
	s.closed = true
	if s.searchCancel != nil {
		s.searchCancel()
	}
	touched := s.searchTouched
	s.mu.Unlock()
	s.finishSearch(touched)
	return s.release()
}
