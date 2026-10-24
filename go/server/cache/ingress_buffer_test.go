package cache

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"testing"
	"time"
)

// Isolate buffering from fsync, keeping the same allocating frame reader and
// retained batch as HTTP ingress. This does not model socket or disk latency.
func BenchmarkIngressFrameBuffer(b *testing.B) {
	l := testLayout(b, false)
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for _, payload := range []int{32, 256 << 10} {
		values := []Value{uuid(at.UnixMilli(), 1), testObject(42), Uint(1, 8), {}, {}, Text(string(bytes.Repeat([]byte("x"), payload))), {}}
		raw, err := l.Encode(nil, values)
		if err != nil {
			b.Fatal(err)
		}
		for _, count := range []int{1, 128} {
			body := bytes.Repeat(raw, count)
			for _, size := range []int{128 << 10, 4 << 10} {
				b.Run(fmt.Sprintf("payload%d/frames%d/buffer%d", payload, count, size), func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(int64(len(body)))
					for b.Loop() {
						input := bufio.NewReaderSize(bytes.NewReader(body), size)
						var batch [][]byte
						for {
							frame, err := l.ReadFrame(input, nil)
							if err == io.EOF {
								break
							}
							if err != nil {
								b.Fatal(err)
							}
							batch = append(batch, frame)
						}
						if len(batch) != count {
							b.Fatal("lost frame")
						}
					}
				})
			}
		}
	}
}
