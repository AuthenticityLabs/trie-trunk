package cache

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

func awaitSeal(t *testing.T, scheduler *SealScheduler) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for scheduler.Stats().Waiting == 0 {
		if time.Now().After(deadline) {
			t.Fatal("seal never queued")
		}
		time.Sleep(time.Millisecond)
	}
}
func awaitResult(t *testing.T, ch <-chan error) {
	t.Helper()
	select {
	case e := <-ch:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("operation blocked")
	}
}

func TestSealWaitDoesNotBlockIngressOrReplay(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(at.Add(time.Minute).UnixMilli())
	scheduler := NewSealScheduler(1)
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, ObjectIndex: true, Sealer: scheduler, Now: func() time.Time { return time.UnixMilli(clock.Load()) }}
	p, e := OpenEndpoint(c)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	s := p.api.Store
	first := testFrame(t, l, at, 1, false)
	second := testFrame(t, l, at.Add(time.Hour), 2, true)
	if _, e = s.Append(Write, first); e != nil {
		t.Fatal(e)
	}
	clock.Store(at.Add(80 * time.Minute).UnixMilli())
	release := scheduler.acquire()
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	flush := make(chan error, 1)
	go func() { flush <- s.Flush() }()
	awaitSeal(t, scheduler)
	done := make(chan error, 1)
	go func() { _, err := s.Append(Write, second); done <- err }()
	awaitResult(t, done)
	if duplicate, err := s.Append(Write, first); err == nil || duplicate {
		t.Fatal("frozen duplicate", duplicate, err)
	}
	// A different event claiming the same revision is still rejected while the
	// old hour is frozen. A new event in that frozen hour is explicitly late.
	dupe := testFrame(t, l, at.Add(time.Hour+time.Second), 1, false)
	if _, err := s.Append(Write, dupe); err == nil {
		t.Fatal("duplicate revision accepted")
	}
	late := testFrame(t, l, at.Add(time.Second), 3, false)
	var lateError *LateEventError
	if _, err := s.Append(Write, late); !errors.As(err, &lateError) {
		t.Fatal("late frozen event", err)
	}
	h, err := p.snapshotHistory(l.Definition.Identity, 1, testObject(42), at, at.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()
	if len(h.locations) != 2 {
		t.Fatalf("frozen history %d", len(h.locations))
	}
	release()
	released = true
	awaitResult(t, flush)
	count := 0
	if err = h.visitBorrowed(context.Background(), func(_ EventReference, raw []byte) error {
		count++
		if !sameSeedData(t, l, raw, first) && !sameSeedData(t, l, raw, second) {
			t.Error("snapshot bytes changed")
		}
		return nil
	}); err != nil || count != 2 {
		t.Fatal(err, count)
	}
	count = 0
	if err = p.History(context.Background(), l.Definition.Identity, 1, testObject(42), at, at.Add(2*time.Hour), func(_ EventReference, _ []byte) error { count++; return nil }); err != nil || count != 2 {
		t.Fatal("published history", count, err)
	}
	s.mu.Lock()
	b := s.branches[Write]
	if b.live.count(0) != 1 || len(b.runs) != 1 {
		t.Error("publication did not preserve new ingress")
	}
	s.mu.Unlock()
}

func TestSealWorkerDoesNotBlockReadersAndCloseWaits(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(at.Add(time.Minute).UnixMilli())
	entered, resume := make(chan struct{}), make(chan struct{})
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return time.UnixMilli(clock.Load()) }, sealHook: func(stage string) error {
		if stage == "payload" {
			close(entered)
			<-resume
		}
		return nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Append(Read, testFrame(t, l, at, 1, false)); e != nil {
		t.Fatal(e)
	}
	clock.Store(at.Add(80 * time.Minute).UnixMilli())
	flush := make(chan error, 1)
	go func() { flush <- s.Flush() }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not reach payload sync")
	}
	appendDone := make(chan error, 1)
	go func() { _, err := s.Append(Read, testFrame(t, l, at.Add(time.Hour), 2, false)); appendDone <- err }()
	awaitResult(t, appendDone)
	readDone := make(chan error, 1)
	go func() {
		n := 0
		err := s.SearchLive(context.Background(), Query{Branch: Read, From: at, To: at.Add(2 * time.Hour)}, func([]byte) error { n++; return nil })
		if err == nil && n != 2 {
			err = errors.New("missing frozen/young data")
		}
		readDone <- err
	}()
	awaitResult(t, readDone)
	closeDone := make(chan error, 1)
	go func() { closeDone <- s.Close() }()
	select {
	case <-closeDone:
		t.Fatal("close freed active worker")
	case <-time.After(10 * time.Millisecond):
	}
	close(resume)
	awaitResult(t, flush)
	awaitResult(t, closeDone)
}

func TestSealCrashPublication(t *testing.T) {
	if root := os.Getenv("TRUNK_SEAL_CRASH_ROOT"); root != "" {
		l := testLayout(t, false)
		at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
		s, e := Open(Config{Root: root, Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(80 * time.Minute) }, sealHook: func(stage string) error {
			if stage == os.Getenv("TRUNK_SEAL_CRASH_STAGE") {
				os.Exit(77)
			}
			return nil
		}})
		if e != nil {
			t.Fatal(e)
		}
		if e = s.Flush(); e != nil {
			t.Fatal(e)
		}
		t.Fatal("crash hook not reached")
	}
	for _, stage := range []string{"payload", "index", "journal"} {
		t.Run(stage, func(t *testing.T) {
			l := testLayout(t, false)
			at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
			now := at.Add(time.Minute)
			c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }}
			s, e := Open(c)
			if e != nil {
				t.Fatal(e)
			}
			raw := testFrame(t, l, at, 1, false)
			if _, e = s.Append(Read, raw); e != nil {
				t.Fatal(e)
			}
			if e = s.Close(); e != nil {
				t.Fatal(e)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestSealCrashPublication$")
			cmd.Env = append(os.Environ(), "TRUNK_SEAL_CRASH_ROOT="+c.Root, "TRUNK_SEAL_CRASH_STAGE="+stage)
			if out, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("child did not crash: %s", out)
			} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 77 {
				t.Fatalf("child failure: %v %s", err, out)
			}
			now = at.Add(80 * time.Minute)
			s, e = Open(c)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			if e = s.Flush(); e != nil {
				t.Fatal(e)
			}
			got := collect(t, s, Read, at, at.Add(time.Hour))
			if len(got) != 1 || !sameSeedData(t, l, got[0], raw) {
				t.Fatal("lost or duplicated record after crash")
			}
		})
	}
}

func TestNativeLiveLimitAndRetainedIdentity(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	now := at.Add(time.Minute)
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, LiveIndexRecords: 1, Now: func() time.Time { return now }}
	s, e := Open(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	first := testFrame(t, l, at, 1, false)
	if _, e = s.Append(Read, first); e != nil {
		t.Fatal(e)
	}
	if duplicate, err := s.Append(Read, first); !errors.Is(err, ErrStagingFull) || duplicate {
		t.Fatal("retry consumed capacity")
	}
	if _, e = s.Append(Read, testFrame(t, l, at.Add(time.Second), 2, false)); !errors.Is(e, ErrStagingFull) {
		t.Fatal(e)
	}
	now = at.Add(80 * time.Minute)
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	now = at.Add(25 * time.Hour)
	if _, e = s.Append(Read, testFrame(t, l, now, 3, false)); e != nil {
		t.Fatal(e)
	}
}

func TestRetentionWaitsForSealPublication(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(at.Add(time.Minute).UnixMilli())
	entered, resume := make(chan struct{}), make(chan struct{})
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return time.UnixMilli(clock.Load()) }, sealHook: func(stage string) error {
		if stage == "payload" {
			close(entered)
			<-resume
		}
		return nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = s.Append(Read, testFrame(t, l, at, 1, false)); e != nil {
		t.Fatal(e)
	}
	clock.Store(at.Add(80 * time.Minute).UnixMilli())
	flush := make(chan error, 1)
	go func() { flush <- s.Flush() }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	retained := make(chan error, 1)
	go func() { s.mu.Lock(); defer s.mu.Unlock(); retained <- s.prune(Read, at.Add(time.Hour)) }()
	select {
	case e := <-retained:
		t.Fatalf("retention overtook unpublished seal: %v", e)
	case <-time.After(10 * time.Millisecond):
	}
	close(resume)
	awaitResult(t, flush)
	awaitResult(t, retained)
	s.mu.Lock()
	defer s.mu.Unlock()
	if b := s.branches[Read]; len(b.runs) != 0 || b.baseRows == 0 {
		t.Fatal("retention did not use published range")
	}
}
