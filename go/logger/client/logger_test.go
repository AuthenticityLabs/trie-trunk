package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"
	Keys "trie-trunk/go/logger/client/keys"

	"trie-trunk/go/codec"
	"trie-trunk/go/models"
)

func testLogger(t testing.TB, w io.Writer, config Config) *Logger {
	t.Helper()
	l, err := New(w, config)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestConcurrentEnrichmentAndFinish(t *testing.T) {
	var out bytes.Buffer
	l := testLogger(t, &out, Config{Format: codec.CompactJSON})
	ctx, b := l.Begin(context.Background())
	if FromContext(ctx) != b {
		t.Fatal("context lost builder")
	}
	var group sync.WaitGroup
	for i := 0; i < 32; i++ {
		group.Add(1)
		go func() { defer group.Done(); b.Checkpoint(1, time.Microsecond).Field("count", codec.Int(42)) }()
	}
	group.Wait()
	b.HttpStatus(200).ResponseBytes(0)
	for i := 0; i < 32; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := b.Finish(); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	if bytes.Count(out.Bytes(), []byte{'\n'}) != 1 {
		t.Fatal("duplicate writes")
	}
	r, err := codec.Decode(bytes.TrimSpace(out.Bytes()), codec.CompactJSON)
	if err != nil {
		t.Fatal(err)
	}
	if r[Keys.ResponseBytes] != json.Number("0") {
		t.Fatalf("explicit zero lost: %v", r)
	}
	before := b.Snapshot()
	b.MessageText("too late").Field("late", codec.Bool(true))
	if !errors.Is(b.Check(), ErrFinalized) || !reflect.DeepEqual(before, b.Snapshot()) {
		t.Fatal("late mutation changed entry")
	}
	if before.Checkpoints[0].Count != 32 {
		t.Fatalf("lost concurrent checkpoints: %+v", before.Checkpoints)
	}
}

func TestSnapshotOwnershipAndLimits(t *testing.T) {
	l := testLogger(t, io.Discard, Config{MaxFields: 2, MaxTextBytes: 8, MaxCheckpoints: 1})
	_, b := l.Begin(context.Background())
	b.Field("password", codec.String("secret")).Field("object", codec.Object(map[string]codec.Value{"ssn": codec.String("123")}))
	b.Field("overflow", codec.Bool(true)).Checkpoint(1, 0).Checkpoint(2, 0)
	e := b.Snapshot()
	e.Fields["password"] = codec.String("changed")
	e.Checkpoints[0].Count = 99
	current := b.Snapshot()
	if current.Dropped != 2 || current.Checkpoints[0].Count != 1 {
		t.Fatalf("wrong bounded state: %+v", current)
	}
	data, err := current.Encode(codec.ReadableJSON, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("secret")) || bytes.Contains(data, []byte("123")) || bytes.Contains(data, []byte("changed")) {
		t.Fatalf("redaction/ownership failure: %s", data)
	}
}

func TestDisabledDetailAndCheckpoint(t *testing.T) {
	l := testLogger(t, io.Discard, Config{})
	_, b := l.Begin(context.Background())
	calls := 0
	b.IfDebug().FieldLazy("expensive", func() codec.Value { calls++; return codec.String("value") })
	FromContext(context.Background()).FieldLazy("none", func() codec.Value { calls++; return codec.String("value") })
	if calls != 0 || b.Snapshot().Fields != nil {
		t.Fatal("disabled builder evaluated detail")
	}
	stop := b.StartCheckpoint(1)
	stop()
	stop()
	if b.Snapshot().Checkpoints[0].Count != 1 {
		t.Fatal("stop counted twice")
	}
}

type shortWriter struct{ calls int }

func (w *shortWriter) Write(p []byte) (int, error) { w.calls++; return len(p) - 1, nil }
func TestWriteFailureIsStable(t *testing.T) {
	w := &shortWriter{}
	l := testLogger(t, w, Config{})
	_, b := l.Begin(context.Background())
	if !errors.Is(b.Finish(), io.ErrShortWrite) || !errors.Is(b.Finish(), io.ErrShortWrite) || w.calls != 1 {
		t.Fatal("failed write retried or hidden")
	}
}

func TestEncodingEquivalence(t *testing.T) {
	d, err := codec.NewDictionary("customers/v1", map[string]map[uint16]string{"service": {1: "customers"}, "resource": {1: "customer"}, "operation": {1: "lookup"}, "checkpoint": {1: "database"}})
	if err != nil {
		t.Fatal(err)
	}
	l := testLogger(t, io.Discard, Config{Dictionary: d})
	_, b := l.Begin(context.Background())
	b.Service(1).Resource(1).Operation(1).HttpMethod(GET).HttpStatus(400).ResponseBytes(0).
		Error(models.New(models.INVALID_INPUT)).Checkpoint(1, time.Millisecond).
		Field("nested", codec.Object(map[string]codec.Value{"literal": codec.String("1"), "list": codec.List(codec.Int(1), codec.Bool(false))}))
	if err := b.Finish(); err != nil {
		t.Fatal(err)
	}
	e := b.Snapshot()
	readable, err := e.Encode(codec.ReadableJSON, d)
	if err != nil {
		t.Fatal(err)
	}
	var want any
	if err := json.Unmarshal(readable, &want); err != nil {
		t.Fatal(err)
	}
	for _, format := range []codec.Format{codec.CompactJSON, codec.PIL} {
		data, err := e.Encode(format, d)
		if err != nil {
			t.Fatal(err)
		}
		r, err := codec.Decode(data, format)
		if err != nil {
			t.Fatal(err)
		}
		expanded, err := json.Marshal(codec.Expand(r, BuildSchema(), d))
		if err != nil {
			t.Fatal(err)
		}
		var got any
		_ = json.Unmarshal(expanded, &got)
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("format %d mismatch:\n%s\n%s", format, readable, expanded)
		}
	}
}

func TestExplicitEmptyAndUnknownReferences(t *testing.T) {
	l := testLogger(t, io.Discard, Config{})
	_, b := l.Begin(context.Background())
	b.Service(65000).OperationText("").HttpStatus(200).ResponseBytes(0)
	e := b.Snapshot()
	for _, format := range []codec.Format{codec.CompactJSON, codec.PIL} {
		data, err := e.Encode(format, nil)
		if err != nil {
			t.Fatal(err)
		}
		r, err := codec.Decode(data, format)
		if err != nil {
			t.Fatal(err)
		}
		readable := codec.Expand(r, BuildSchema(), nil)
		if value, ok := readable["operation"]; !ok || value != "" {
			t.Fatalf("lost explicit empty: %v", readable)
		}
		if readable["service"] != uint16(65000) {
			t.Fatalf("lost unknown reference: %v", readable)
		}
	}
}

func TestBoundedErrorMetadata(t *testing.T) {
	l := testLogger(t, io.Discard, Config{MaxTextBytes: 4})
	_, b := l.Begin(context.Background())
	err := models.New(models.INVALID_INPUT).WithMessage("long public message")
	b.Error(err)
	if b.Snapshot().Error.Message().Raw() != "long" || err.Message().Raw() != "long public message" {
		t.Fatal("error not bounded or original mutated")
	}
}

func TestEntryIdCarriesStartAndRemainsStable(t *testing.T) {
	var out bytes.Buffer
	l := testLogger(t, &out, Config{Format: codec.PIL})
	before := time.Now()
	_, b := l.Begin(context.Background())
	after := time.Now()
	id := b.Id()
	if !id.Valid() || uuidMillis(id) < before.UnixMilli() || uuidMillis(id) > after.UnixMilli() {
		t.Fatalf("wrong transaction start: %s", id)
	}
	if err := b.Finish(); err != nil {
		t.Fatal(err)
	}
	e := b.Snapshot()
	if e.Id != id || b.Id() != id || e.Duration < 0 {
		t.Fatal("identity or duration relationship changed")
	}
	r, err := codec.Decode(out.Bytes(), codec.PIL)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := r[codec.Key(11)]; exists {
		t.Fatal("redundant start time on wire")
	}
	raw, ok := r[Keys.Id].([]byte)
	if !ok || len(raw) != 16 || codec.UUIDv7(raw) != id {
		t.Fatalf("wrong binary Id: %v", r[Keys.Id])
	}
	if r[Keys.Duration] != uint64(e.Duration.Microseconds()) {
		t.Fatalf("wrong duration: %v", r[Keys.Duration])
	}
	_, other := l.Begin(context.Background())
	if other.Id() == id {
		t.Fatal("new transaction reused Id")
	}
	first, err := e.Encode(codec.PIL, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.Encode(codec.PIL, nil)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("re-encoding regenerated identity")
	}
}

func TestIdentityFailureDoesNotEmit(t *testing.T) {
	var out bytes.Buffer
	l := testLogger(t, &out, Config{Format: codec.PIL})
	_, b := l.Begin(context.Background())
	failure := errors.New("entropy unavailable")
	b.state.initErr = failure
	if !errors.Is(b.Check(), failure) || !errors.Is(b.Finish(), failure) || out.Len() != 0 {
		t.Fatal("identity initialization failure was hidden")
	}
}

func TestFinishRacesWithEnrichment(t *testing.T) {
	l := testLogger(t, io.Discard, Config{Format: codec.PIL})
	_, b := l.Begin(context.Background())
	var group sync.WaitGroup
	for i := 0; i < 16; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for j := 0; j < 50; j++ {
				b.Field("value", codec.Int(int64(j))).Checkpoint(1, time.Microsecond)
				_ = b.Snapshot()
			}
		}()
	}
	if err := b.Finish(); err != nil {
		t.Fatal(err)
	}
	group.Wait()
}

func uuidMillis(id codec.UUIDv7) int64 {
	return int64(id[0])<<40 | int64(id[1])<<32 | int64(id[2])<<24 | int64(id[3])<<16 | int64(id[4])<<8 | int64(id[5])
}
