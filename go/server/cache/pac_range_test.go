package cache

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestPACBoundaryWalkEmptyBlankAndYear(t *testing.T) {
	from := time.Date(2025, 12, 31, 22, 0, 0, 0, time.UTC).UnixMilli()
	runs := []Run{timeRun(from+TimeHourMS, 0, 1), timeRun(from+5*TimeHourMS, 2, 1)}
	const size = 128
	for i := range runs {
		runs[i].PodsStart = headerSize + uint64(i*size)
		runs[i].PodsEnd = runs[i].PodsStart + size
	}
	tree, err := buildTimeDirectory(runs, from, from+8*TimeHourMS, 4, [16]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(t.TempDir(), "pods")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	raw := make([]byte, headerSize+size*2)
	for _, r := range runs {
		order.PutUint32(raw[r.PodsStart:], size)
	}
	if _, err = f.Write(raw); err != nil {
		t.Fatal(err)
	}
	for h, want := range []uint64{headerSize, headerSize, headerSize + size, headerSize + size, headerSize + size, headerSize + size, headerSize + 2*size, headerSize + 2*size, headerSize + 2*size} {
		got, err := tree.podsBoundary(f, 0, from+int64(h)*TimeHourMS)
		if err != nil || got != want {
			t.Fatalf("hour %d: %d %v", h, got, err)
		}
	}
	// Empty sealed siblings need no occupancy byte and preserve PAC boundaries.
	got, err := tree.podsBoundary(f, 0, from+4*TimeHourMS)
	if err != nil || got != headerSize+size {
		t.Fatal(got, err)
	}
	if _, err = tree.window(from+2*TimeHourMS, from+9*TimeHourMS, false); err == nil {
		t.Fatal("unpublished hour certified as empty")
	}
	if _, err = tree.podsBoundary(f, 0, from+9*TimeHourMS); err == nil {
		t.Fatal("future boundary accepted")
	}
	// Logical byte addresses retain their origin after physical prefix removal.
	for i := range runs {
		runs[i].PodsStart += 4096
		runs[i].PodsEnd += 4096
	}
	tree, err = buildTimeDirectory(runs, from, from+8*TimeHourMS, 4, [16]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	got, err = tree.podsBoundary(f, 4096, from+4*TimeHourMS)
	if err != nil || got != 4096+headerSize+size {
		t.Fatal(got, err)
	}
}

func TestPACRangeStreamingDoesNotInspectInteriorPacks(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	s, err := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(24 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, h := range []int{0, 2, 4} {
		if _, err = s.Append(Write, testFrame(t, l, at.Add(time.Duration(h)*time.Hour), uint32(h+1), false)); err != nil {
			t.Fatal(err)
		}
	}
	req := CollectionRequest{Branch: Write, At: at, Until: at.Add(6 * time.Hour)}
	var manifest Manifest
	var packs []NativeChunk
	if err = s.Collect(context.Background(), req, func(m Manifest) error { manifest = m; return nil }, func(c NativeChunk) error {
		if c.Kind == "pac" {
			packs = append(packs, c)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(packs) != 3 {
		t.Fatal(len(packs))
	}
	b := s.branches[Write]
	middle := b.runs[1]
	// Deliberately unreadable interior length: transfer must not parse it. The
	// receiver's sealed checksum rejects corruption; the sender only copies bytes.
	if _, err = b.pods.WriteAt([]byte{0, 0, 0, 0}, int64(middle.PodsStart-b.podsRetired)); err != nil {
		t.Fatal(err)
	}
	packs[1].Data[0] = 0
	packs[1].Data[1] = 0
	packs[1].Data[2] = 0
	packs[1].Data[3] = 0
	var joined bytes.Buffer
	for _, p := range packs {
		joined.Write(p.Data)
	}
	ranges := 0
	run := func(have Coverage) []byte {
		t.Helper()
		req.Have = have
		var got bytes.Buffer
		err = s.collectDelivery(context.Background(), req, func(Manifest) error { return nil }, func(c NativeChunk) error {
			if c.Kind == "pac" {
				t.Fatal("per-pack callback")
			}
			return nil
		}, func(f *os.File, start, end uint64) error {
			ranges++
			_, e := io.Copy(&got, io.NewSectionReader(f, int64(start), int64(end-start)))
			return e
		})
		if err != nil {
			t.Fatal(err)
		}
		return got.Bytes()
	}
	if got := run(Coverage{}); ranges != 1 || !bytes.Equal(got, joined.Bytes()) {
		t.Fatal("range not copied verbatim", ranges)
	}
	// Covering the middle hour splits the physical response into two ranges.
	m := manifest.Blocks[1]
	ranges = 0
	have := Coverage{Definition: manifest.Definition, Generation: manifest.Generation, Index: []Span{{m.Start, m.End}}, MemberRanges: []Span{{m.PAC.Placement.MemberStart, m.PAC.Placement.MemberStart + m.PAC.Placement.MemberBytes}}}
	if got := run(have); ranges != 2 || !bytes.Equal(got, append(packs[0].Data, packs[2].Data...)) {
		t.Fatal("covered PAC was resent", ranges)
	}
	// Restore the pack for the end-to-end HTTP parser.
	if _, err = b.pods.WriteAt(order.AppendUint32(nil, uint32(middle.PodsEnd-middle.PodsStart)), int64(middle.PodsStart-b.podsRetired)); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/collect?tap=demo/payment/transactions.v1/write&from="+at.Format(time.RFC3339)+"&to="+at.Add(6*time.Hour).Format(time.RFC3339), nil)
	r.Header.Set("Accept", "application/pil")
	r.Header.Set("PIL-Version", "2")
	r.Header.Set("PIL-Features", "native-ranges,adaptive-integers")
	w := httptest.NewRecorder()
	(&HTTP{Store: s}).nativeCollect(w, r)
	ms, chunks := readNativeTest(t, w.Body.Bytes())
	if len(ms) != 1 || len(ms[0].Blocks) != 3 {
		t.Fatal("sealed windows not merged", len(ms))
	}
	n := 0
	for _, c := range chunks {
		if c.Kind == "pac" {
			n++
		}
	}
	if n != 3 {
		t.Fatal(n)
	}
	if out := os.Getenv("PIL_RANGE_FIXTURE"); out != "" {
		if err = os.WriteFile(out, w.Body.Bytes(), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPACRangeShortReadAndCancellation(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "pods")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.Write([]byte{1, 2, 3})
	if err = writePACRange(context.Background(), io.Discard, 0, f, 0, 4); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = writePACRange(ctx, io.Discard, 0, f, 0, 3); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestPACRangePinsFileThroughRetention(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	now := at.Add(2 * time.Hour)
	s, err := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.policy.Retention.Write = "1h"
	if _, err = s.Append(Write, testFrame(t, l, at, 1, false)); err != nil {
		t.Fatal(err)
	}
	req := CollectionRequest{Branch: Write, At: at, Until: at.Add(time.Hour)}
	var expected []byte
	if err = s.Collect(context.Background(), req, func(Manifest) error { return nil }, func(c NativeChunk) error {
		if c.Kind == "pac" {
			expected = append(expected, c.Data...)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	err = s.collectDelivery(context.Background(), req, func(Manifest) error {
		now = at.Add(6 * time.Hour)
		if _, e := s.Append(Write, testFrame(t, l, now, 2, false)); e != nil {
			return e
		}
		return s.ApplyRetention()
	}, func(NativeChunk) error { return nil }, func(f *os.File, start, end uint64) error {
		_, e := io.Copy(&got, io.NewSectionReader(f, int64(start), int64(end-start)))
		return e
	})
	if err != nil || len(expected) == 0 || !bytes.Equal(got.Bytes(), expected) {
		t.Fatal("pinned range changed", err)
	}
}

func BenchmarkPACRangeDelivery(b *testing.B) {
	const hours = 256
	const events = 256
	l := testLayout(b, false)
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	now := at.Add((hours + 2) * time.Hour)
	s, err := Open(Config{Root: b.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	off := false
	s.policy.SearchIndex = &off
	s.mu.Lock()
	locked := true
	defer func() {
		if locked {
			s.mu.Unlock()
		}
	}()
	branch, err := s.openBranch(Error)
	if err != nil {
		b.Fatal(err)
	}
	for h := 0; h < hours; h++ {
		batch := make([]pending, events)
		for n := 0; n < events; n++ {
			stamp := at.Add(time.Duration(h)*time.Hour + time.Duration(n)*time.Millisecond)
			ordinal := uint32(h*events + n + 1)
			raw := testFrame(b, l, stamp, ordinal, false)
			id, e := eventID(uuid(stamp.UnixMilli(), ordinal))
			if e != nil {
				b.Fatal(e)
			}
			var frame Frame
			if err = l.Scan(raw, &frame); err != nil {
				b.Fatal(err)
			}
			if err = s.accumulate(branch, id, frame.Values); err != nil {
				b.Fatal(err)
			}
			batch[n] = pending{id, raw}
			branch.pendingBytes += len(raw)
		}
		if err = s.commitBatch(branch, batch); err != nil {
			b.Fatal(err)
		}
	}
	branch.finalizedBefore = at.Add(hours * time.Hour).UnixMilli()
	if err = s.publishTime(branch); err != nil {
		b.Fatal(err)
	}
	s.mu.Unlock()
	locked = false
	req := CollectionRequest{Branch: Error, At: at, Until: at.Add(hours * time.Hour)}
	for _, mode := range []string{"per-pack", "contiguous"} {
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(branch.podsEnd - headerSize))
			for n := 0; n < b.N; n++ {
				var sendRange func(*os.File, uint64, uint64) error
				if mode == "contiguous" {
					sendRange = func(f *os.File, start, end uint64) error {
						return writePACRange(context.Background(), io.Discard, 0, f, start, end)
					}
				}
				err := s.collectDelivery(context.Background(), req, func(Manifest) error { return nil }, func(NativeChunk) error { return nil }, sendRange)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
