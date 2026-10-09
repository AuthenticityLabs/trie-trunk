package codec

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"testing/iotest"
)

func TestFrameBoundariesAndFragmentedStream(t *testing.T) {
	var stream []byte
	for _, size := range []int{1, 255, 256, MaxFramePayload} {
		payload := bytes.Repeat([]byte{0xa1}, size)
		frame, err := AppendFrame(nil, payload)
		if err != nil {
			t.Fatal(err)
		}
		if len(frame) != size+2 || frame[0] != byte(size>>8) || frame[1] != byte(size) {
			t.Fatalf("incorrect prefix for %d", size)
		}
		stream = append(stream, frame...)
	}
	r := iotest.OneByteReader(bytes.NewReader(stream))
	for _, size := range []int{1, 255, 256, MaxFramePayload} {
		payload, err := ReadFrame(r, MaxFramePayload)
		if err != nil || len(payload) != size || !bytes.Equal(payload, bytes.Repeat([]byte{0xa1}, size)) {
			t.Fatalf("fragmented frame %d: %v", size, err)
		}
	}
	if _, err := ReadFrame(r, MaxFramePayload); !errors.Is(err, io.EOF) {
		t.Fatalf("expected clean EOF: %v", err)
	}
}

func TestFrameRejectsOversizeAndTruncation(t *testing.T) {
	for _, payload := range [][]byte{nil, make([]byte, MaxFramePayload+1)} {
		if _, err := AppendFrame(nil, payload); !errors.Is(err, ErrFrameSize) {
			t.Fatalf("accepted invalid length: %v", err)
		}
	}
	for _, frame := range [][]byte{{0}, {0, 2}, {0, 2, 1}} {
		if _, err := ReadFrame(bytes.NewReader(frame), MaxFramePayload); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("accepted truncated frame %x: %v", frame, err)
		}
	}
	for _, frame := range [][]byte{{0, 0}, {0, 20}} {
		if _, err := ReadFrame(bytes.NewReader(frame), 10); !errors.Is(err, ErrFrameSize) {
			t.Fatalf("invalid size not rejected before body read: %v", err)
		}
	}
}

func TestAppendFrameReusesCapacity(t *testing.T) {
	buffer := make([]byte, 0, 64)
	payload := []byte{0xa1, 2, 1}
	if allocs := testing.AllocsPerRun(100, func() {
		var err error
		buffer, err = AppendFrame(buffer[:0], payload)
		if err != nil {
			panic(err)
		}
	}); allocs != 0 {
		t.Fatalf("warm framing allocates: %g", allocs)
	}
}
