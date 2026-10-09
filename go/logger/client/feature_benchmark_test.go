package logging

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"trie-trunk/go/codec"
	"trie-trunk/go/models"
)

// Isolate optional features; the earlier Extensions benchmark combines a field
// and checkpoint and cannot attribute their individual costs.
func BenchmarkFeatureCost(b *testing.B) {
	message := strings.Repeat("x", 512)
	for _, tc := range []struct {
		name   string
		enrich func(*Builder)
	}{
		{"Baseline", func(*Builder) {}},
		{"Checkpoint1", func(tx *Builder) { tx.Checkpoint(1, time.Millisecond) }},
		{"Checkpoint8", func(tx *Builder) {
			for i := 1; i <= 8; i++ {
				tx.Checkpoint(CheckpointId(i), time.Millisecond)
			}
		}},
		{"Checkpoint32", func(tx *Builder) {
			for i := 1; i <= 32; i++ {
				tx.Checkpoint(CheckpointId(i), time.Millisecond)
			}
		}},
		{"SameCheckpoint32", func(tx *Builder) {
			for i := 0; i < 32; i++ {
				tx.Checkpoint(1, time.Millisecond)
			}
		}},
		{"CheckpointClosure", func(tx *Builder) { stop := tx.StartCheckpoint(1); stop() }},
		{"ScalarField", func(tx *Builder) { tx.Field("account_id", codec.String("abc")) }},
		{"NestedFields", func(tx *Builder) {
			tx.Field("details", codec.Object(map[string]codec.Value{"account_id": codec.String("abc"), "region": codec.String("east"), "count": codec.Int(3)}))
		}},
		{"KnownError", func(tx *Builder) { tx.Error(models.New(models.INVALID_INPUT)) }},
		{"LiteralMessage512", func(tx *Builder) { tx.MessageText(message) }},
	} {
		b.Run(tc.name, func(b *testing.B) {
			l := testLogger(b, io.Discard, Config{Format: codec.CBOR, Service: codec.Ref(1), Version: "1.0.0"})
			build := func() *Builder {
				_, tx := l.Begin(context.Background())
				tx.Resource(1).Operation(1).HttpMethod(GET).HttpStatus(200).ResponseBytes(128)
				tc.enrich(tx)
				return tx
			}
			sample := build()
			if err := sample.Finish(); err != nil {
				b.Fatal(err)
			}
			e := sample.Snapshot()
			e.Duration = 8432 * time.Microsecond
			data, err := e.Encode(codec.CBOR, nil)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := build().Finish(); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(len(data)), "payload-bytes")
		})
	}
}

func TestModelMemorySizes(t *testing.T) {
	t.Logf("fixed Go sizes: Entry=%d state=%d Symbol=%d Checkpoint=%d bytes; backing allocations excluded",
		reflect.TypeOf(Entry{}).Size(), reflect.TypeOf(state{}).Size(), reflect.TypeOf(codec.Symbol{}).Size(), reflect.TypeOf(Checkpoint{}).Size())
}
