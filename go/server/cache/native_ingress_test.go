package cache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

func ingressFixture(t testing.TB, size int) (*Layout, []byte) {
	l := testLayout(t, false)
	raw := testFrame(t, l, time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC), 1, false)
	var f Frame
	if err := l.Scan(raw, &f); err != nil {
		t.Fatal(err)
	}
	f.Values[5] = Value{Offset, bytes.Repeat([]byte("x"), size)}
	raw, err := l.Encode(nil, f.Values)
	if err != nil {
		t.Fatal(err)
	}
	return l, raw
}
func TestNativeIngressPublishedSpansAndReuse(t *testing.T) {
	l, raw := ingressFixture(t, 78)
	m := newNativeMemory(1 << 20)
	defer m.close()
	store := &Store{memory: m}
	branch := &branchStore{}
	first := &nativeIngress{memory: m}
	a, err := first.read(l, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	owned, err := branch.own(store, [16]byte{}, a.raw, a.buffer)
	if err != nil {
		t.Fatal(err)
	}
	pin := branch.pinMemory()
	defer pin()
	first.close()
	second := &nativeIngress{memory: m}
	b, err := second.read(l, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if a.buffer != b.buffer {
		t.Fatal("request did not reuse native tail")
	}
	if &a.raw[0] == &b.raw[0] {
		t.Fatal("published span reused")
	}
	if _, err = branch.own(store, [16]byte{}, b.raw, b.buffer); err != nil {
		t.Fatal(err)
	}
	if len(branch.memory[0].adopted) != 1 {
		t.Fatal("adopted one chunk more than once")
	}
	second.close()
	for _, arena := range branch.memory {
		arena.close()
	}
	m.close()
	if !bytes.Equal(owned, raw) || !bytes.Equal(b.raw, raw) {
		t.Fatal("pin did not preserve shared chunk")
	}
}
func TestNativeIngressGrowthAndTruncatedTail(t *testing.T) {
	l, small := ingressFixture(t, 78)
	_, large := ingressFixture(t, ingressChunkBytes+1000)
	m := newNativeMemory(1 << 20)
	defer m.close()
	r := &nativeIngress{memory: m}
	defer r.close()
	a, err := r.read(l, bytes.NewReader(small))
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.read(l, bytes.NewReader(large))
	if err != nil {
		t.Fatal(err)
	}
	if a.buffer == b.buffer || len(r.retired) == 0 {
		t.Fatal("did not exercise chunk growth")
	}
	if !bytes.Equal(a.raw, small) || !bytes.Equal(b.raw, large) {
		t.Fatal("growth damaged an uncommitted frame")
	}
	branch := &branchStore{}
	store := &Store{memory: m}
	for _, frame := range []nativeInput{a, b} {
		if _, err = branch.own(store, [16]byte{}, frame.raw, frame.buffer); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, arena := range branch.memory {
			arena.close()
		}
	}()
	r.staged()
	if !bytes.Equal(a.raw, small) {
		t.Fatal("stage freed retained chunk")
	}
	if _, err = r.read(l, bytes.NewReader(small[:len(small)-1])); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncation %v", err)
	}
	c, err := r.read(l, bytes.NewReader(small))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c.raw, small) || !bytes.Equal(b.raw, large) {
		t.Fatal("retry overwrote published data")
	}
}
func TestNativeIngressBudgetKeepsPrefix(t *testing.T) {
	l, raw := ingressFixture(t, 78)
	m := newNativeMemory(256)
	defer m.close()
	r := &nativeIngress{memory: m}
	defer r.close()
	a, err := r.read(l, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.read(l, bytes.NewReader(raw)); !errors.Is(err, ErrStagingFull) {
		t.Fatalf("budget %v", err)
	}
	if !bytes.Equal(a.raw, raw) {
		t.Fatal("budget failure damaged prefix")
	}
}
func TestNativeIngressConcurrentLeases(t *testing.T) {
	l, raw := ingressFixture(t, 78)
	m := newNativeMemory(8 << 20)
	defer m.close()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				r := &nativeIngress{memory: m}
				f, err := r.read(l, bytes.NewReader(raw))
				if err != nil || !bytes.Equal(f.raw, raw) {
					t.Errorf("concurrent ingress: %v", err)
				}
				r.close()
			}
		}()
	}
	wg.Wait()
	if m.stats().Used > ingressIdleChunks*ingressChunkBytes {
		t.Fatal("idle cache exceeds bound")
	}
}

// Single means one event per request; batch is amortized framing, not a wait
// policy. No fsync/network/semantic validation is included. The original and
// rejected per-record-native paths remain as controls in the same executable.
func BenchmarkNativeIngressChunks(b *testing.B) {
	for _, size := range []int{78, 4096, 65536} {
		l, raw := ingressFixture(b, size)
		for _, count := range []int{1, 32, 1024} {
			for _, mode := range []string{"go", "native-record", "native-chunk"} {
				b.Run(fmt.Sprintf("value=%d/events=%d/%s", size, count, mode), func(b *testing.B) {
					m := newNativeMemory(16 << 20)
					defer m.close()
					reader := bytes.NewReader(raw)
					b.SetBytes(int64(len(raw) * count))
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						r := nativeIngress{memory: m}
						for j := 0; j < count; j++ {
							reader.Reset(raw)
							var got []byte
							var err error
							switch mode {
							case "go":
								got, err = l.ReadFrame(reader, nil)
							case "native-record":
								buffer := m.buffer()
								got, err = l.ReadFrameBuffer(reader, nil, true, buffer.resize)
								if err != nil || !bytes.Equal(got, raw) {
									b.Fatal(err)
								}
								buffer.close()
								continue
							case "native-chunk":
								var f nativeInput
								f, err = r.read(l, reader)
								r.staged()
								got = f.raw
							}
							if err != nil || !bytes.Equal(got, raw) {
								b.Fatal(err)
							}
						}
						r.close()
					}
					b.StopTimer()
					b.ReportMetric(float64(m.stats().Peak), "native-peak-B")
				})
			}
		}
	}
}

func BenchmarkNativeIngressParallel(b *testing.B) {
	l, raw := ingressFixture(b, 78)
	for _, mode := range []string{"go", "native-record", "native-chunk"} {
		b.Run(mode, func(b *testing.B) {
			m := newNativeMemory(64 << 20)
			defer m.close()
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			b.RunParallel(func(pb *testing.PB) {
				reader := bytes.NewReader(raw)
				for pb.Next() {
					reader.Reset(raw)
					var got []byte
					var err error
					switch mode {
					case "go":
						got, err = l.ReadFrame(reader, nil)
					case "native-record":
						buffer := m.buffer()
						got, err = l.ReadFrameBuffer(reader, nil, true, buffer.resize)
						if err != nil || !bytes.Equal(got, raw) {
							b.Fatal(err)
						}
						buffer.close()
						continue
					case "native-chunk":
						r := nativeIngress{memory: m}
						f, e := r.read(l, reader)
						got, err = f.raw, e
						if err != nil || !bytes.Equal(got, raw) {
							b.Fatal(err)
						}
						r.close()
						continue
					}
					if err != nil || !bytes.Equal(got, raw) {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

func TestNativeIngressCloseWithLeasedChunks(t *testing.T) {
	l, raw := ingressFixture(t, 78)
	m := newNativeMemory(2 << 20)
	var ready, done sync.WaitGroup
	ready.Add(8)
	done.Add(8)
	closed := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer done.Done()
			r := &nativeIngress{memory: m}
			f, err := r.read(l, bytes.NewReader(raw))
			ready.Done()
			<-closed
			if err != nil || !bytes.Equal(f.raw, raw) {
				t.Errorf("close invalidated lease: %v", err)
			}
			r.close()
		}()
	}
	ready.Wait()
	m.close()
	close(closed)
	done.Wait()
	r := &nativeIngress{memory: m}
	defer r.close()
	if _, err := r.read(l, bytes.NewReader(raw)); err == nil {
		t.Fatal("allocated after close")
	}
}

func BenchmarkNativeIngressSparse(b *testing.B) {
	l := testLayout(b, false)
	d := l.Definition
	for i := 0; i < 5; i++ {
		d.Fields = append(d.Fields, Field{Name: fmt.Sprintf("extra%d", i), Type: "STRING"})
	}
	l, err := Compile(d)
	if err != nil {
		b.Fatal(err)
	}
	for _, changed := range []int{1, 2} {
		values := make([]Value, len(l.Definition.Fields))
		values[0], values[1], values[2] = uuid(1792843200000, 1), testObject(42), Uint(1, 8)
		values[4] = Uint(1, 1)
		if changed == 2 {
			values[6] = Uint(99, 8)
		}
		raw, err := l.Encode(nil, values)
		if err != nil {
			b.Fatal(err)
		}
		for _, mode := range []string{"go", "native-record", "native-chunk"} {
			b.Run(fmt.Sprintf("fields=12/changed=%d/%s", changed, mode), func(b *testing.B) {
				m := newNativeMemory(4 << 20)
				defer m.close()
				reader := bytes.NewReader(raw)
				b.ReportAllocs()
				b.SetBytes(int64(len(raw)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					reader.Reset(raw)
					var got []byte
					var e error
					switch mode {
					case "go":
						got, e = l.ReadFrame(reader, nil)
					case "native-record":
						buffer := m.buffer()
						got, e = l.ReadFrameBuffer(reader, nil, true, buffer.resize)
						if e != nil || !bytes.Equal(got, raw) {
							b.Fatal(e)
						}
						buffer.close()
						continue
					case "native-chunk":
						r := nativeIngress{memory: m}
						f, err := r.read(l, reader)
						got, e = f.raw, err
						if e != nil || !bytes.Equal(got, raw) {
							b.Fatal(e)
						}
						r.close()
						continue
					}
					if e != nil || !bytes.Equal(got, raw) {
						b.Fatal(e)
					}
				}
			})
		}
	}
}

func TestNativeIngressHTTPBatchRecovery(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(5 * time.Minute) }}
	s, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	frames := make([][]byte, 2050)
	var body []byte
	for i := range frames {
		frames[i] = testFrame(t, l, at.Add(time.Duration(i)*time.Millisecond), uint32(i+1), i%2 == 1)
		if i < len(frames)-1 {
			body = append(body, frames[i]...)
		}
	}
	body = append(body, frames[len(frames)-1][:len(frames[len(frames)-1])-1]...)
	h := &HTTP{Store: s}
	submit := func(raw []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/ingest?tap="+url.QueryEscape(h.tap(Write)), bytes.NewReader(raw))
		r.Header.Set("PIL-Version", "2")
		r.Header.Set("Definition-Version", fmtDigest(l))
		r.Header.Set("Content-Type", "application/pil")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := submit(body); w.Code != 400 || w.Header().Get("Trunk-Received") != "2049" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	if w := submit(frames[2049]); w.Code != 200 || w.Header().Get("Trunk-Received") != "1" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	err = s.SearchLive(context.Background(), Query{Branch: Write, From: at, To: at.Add(time.Minute)}, func(raw []byte) error {
		if n >= len(frames) || !bytes.Equal(raw, frames[n]) {
			t.Fatal("batch/recovery changed a frame", n)
		}
		n++
		return nil
	})
	if err != nil || n != len(frames) {
		t.Fatal(n, err)
	}
}
