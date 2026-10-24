package cache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeMemoryAdoptionPinAndBudget(t *testing.T) {
	m := newNativeMemory(256)
	defer m.close()
	input := m.buffer()
	raw, err := input.resize(80)
	if err != nil {
		t.Fatal(err)
	}
	copy(raw, bytes.Repeat([]byte{71}, 80))
	b := &branchStore{}
	s := &Store{memory: m}
	owned, err := b.own(s, [16]byte{}, raw, input)
	if err != nil {
		t.Fatal(err)
	}
	if &owned[0] != &raw[0] {
		t.Fatal("adoption copied payload")
	}
	if _, err = input.resize(1); err == nil {
		t.Fatal("adopted frame remains mutable")
	}
	release := b.pinMemory()
	input.close()
	for _, a := range b.memory {
		a.close()
	}
	if m.stats().Used != 80 || !bytes.Equal(owned, bytes.Repeat([]byte{71}, 80)) {
		t.Fatal("pin lost bytes")
	}
	blocked := m.buffer()
	defer blocked.close()
	if _, err = blocked.resize(200); !errors.Is(err, ErrStagingFull) {
		t.Fatalf("budget: %v", err)
	}
	release()
	if m.stats().Used != 0 {
		t.Fatal("pin leaked")
	}
	if _, err = blocked.resize(200); err != nil {
		t.Fatal(err)
	}
	blocked.close()
	if m.stats().Used != 0 {
		t.Fatal("buffer leaked")
	}
}
func TestNativeMemoryPoolCloseWithReader(t *testing.T) {
	m := newNativeMemory(256)
	b := m.buffer()
	raw, err := b.resize(100)
	if err != nil {
		t.Fatal(err)
	}
	raw[0] = 42
	m.close()
	if raw[0] != 42 {
		t.Fatal("pool close invalidated active buffer")
	}
	b.close()
}
func TestSealSchedulerBoundsConcurrency(t *testing.T) {
	scheduler := NewSealScheduler(2)
	var wg sync.WaitGroup
	var active, peak atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := scheduler.acquire()
			defer release()
			n := active.Add(1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
		}()
	}
	wg.Wait()
	stats := scheduler.Stats()
	if peak.Load() > 2 || stats.Active != 0 || stats.Waiting != 0 || stats.Attempts != 20 {
		t.Fatalf("invalid scheduler: %+v peak=%d", stats, peak.Load())
	}
}

func TestNativeHistorySurvivesStoreClose(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	p, err := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, ObjectIndex: true, Now: func() time.Time { return at.Add(time.Minute) }})
	if err != nil {
		t.Fatal(err)
	}
	raw := testFrame(t, l, at, 1, false)
	if _, err = p.api.Store.Append(Write, raw); err != nil {
		t.Fatal(err)
	}
	h, err := p.snapshotHistory(l.Definition.Identity, 1, testObject(42), at, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	if err = h.visitBorrowed(context.Background(), func(_ EventReference, b []byte) error {
		if &b[0] != &h.locations[0].raw[0] {
			t.Fatal("borrowed history copied payload")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	n := 0
	if err = h.visit(context.Background(), func(_ EventReference, b []byte) error {
		n++
		if !bytes.Equal(b, raw) {
			t.Fatal("snapshot changed after close")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows=%d", n)
	}
}

func BenchmarkIngressBufferOwnership(b *testing.B) {
	l := testLayout(b, false)
	raw := testFrame(b, l, time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC), 1, false)
	for _, size := range []int{len(raw), 4096, 65536} {
		var frame Frame
		if err := l.Scan(raw, &frame); err != nil {
			b.Fatal(err)
		}
		frame.Values[5] = Value{Offset, bytes.Repeat([]byte("x"), size)}
		input, err := l.Encode(nil, frame.Values)
		if err != nil {
			b.Fatal(err)
		}
		for _, native := range []bool{false, true} {
			b.Run(fmt.Sprintf("%d/native=%t", size, native), func(b *testing.B) {
				pool := newNativeMemory(1 << 28)
				defer pool.close()
				reader := bytes.NewReader(input)
				b.SetBytes(int64(len(input)))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					reader.Reset(input)
					var out []byte
					if native {
						buffer := pool.buffer()
						out, err = l.readFrameBuffer(reader, nil, true, buffer.resize)
						if err != nil || !bytes.Equal(out, input) {
							b.Fatal(err)
						}
						buffer.close()
					} else {
						out, err = l.ReadFrame(reader, nil)
						if err != nil || !bytes.Equal(out, input) {
							b.Fatal(err)
						}
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(pool.stats().Peak), "native-peak-B")
			})
		}
	}
}

func TestSealWindowAccounting(t *testing.T) {
	s := NewSealScheduler(1)
	end := time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC)
	s.observeWindow(end, end.Add(59*time.Minute))
	s.observeWindow(end, end.Add(61*time.Minute))
	if stats := s.Stats(); stats.OverWindow != 1 || stats.MaxOverrun != time.Minute {
		t.Fatalf("%+v", stats)
	}
}
