package logging

import (
	"bytes"
	"context"
	"testing"
	Keys "trie-trunk/go/logger/client/keys"

	"trie-trunk/go/codec"
	"trie-trunk/go/logger/out"
)

// Local stub: schema control frames and record frames are deliberately separate.
// A future transport adapter implements these operations over its connection.
type localLogWriter struct {
	revision   codec.Revision
	definition codec.Definition
	records    [][]byte
	checks     int
	pushes     int
}

func (w *localLogWriter) Negotiate(_ context.Context, wanted codec.Revision) (codec.Revision, error) {
	w.checks++
	return w.revision, nil
}
func (w *localLogWriter) PushSchema(_ context.Context, wanted codec.Revision, data []byte) (codec.Revision, error) {
	d, err := decodeDefinitionFrame(data, wanted)
	if err != nil {
		return codec.Revision{}, err
	}
	w.definition = d
	w.revision = wanted
	w.pushes++
	return wanted, nil
}
func (w *localLogWriter) WriteFrame(data []byte) error {
	w.records = append(w.records, append([]byte(nil), data...))
	return nil
}
func (w *localLogWriter) Close() error { return nil }

func TestStreamInitializationAndFramedRecords(t *testing.T) {
	dictionary, err := codec.NewDictionary("customers/v1", map[string]map[uint16]string{"service": {1: "customers"}})
	if err != nil {
		t.Fatal(err)
	}
	peer := &localLogWriter{}
	l, err := NewStream(context.Background(), out.ConnectFunc(func(context.Context) (out.Connection, error) { return peer, nil }), Config{Dictionary: dictionary, Service: codec.Ref(1)})
	if err != nil {
		t.Fatal(err)
	}
	if peer.checks != 1 || peer.pushes != 1 || len(peer.records) != 0 {
		t.Fatal("initialization did not finish schema synchronization")
	}
	if peer.definition.Fields[Keys.Error].Object[0].Enums[7] != "INVALID_INPUT" || peer.definition.Domains["service"][1] != "customers" || peer.definition.Fields[Keys.Duration].Unit != "us" {
		t.Fatal("schema did not include nested definitions, values and units")
	}
	for i := 0; i < 2; i++ {
		_, entry := l.Begin(context.Background())
		entry.HttpMethod(GET).HttpStatus(200).ResponseBytes(0)
		if err := entry.Finish(); err != nil {
			t.Fatal(err)
		}
	}
	if peer.checks != 1 || peer.pushes != 1 || len(peer.records) != 2 {
		t.Fatal("schema work leaked into record path")
	}
	for _, data := range peer.records {
		r, err := decodeRecordFrame(data)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := r[codec.Key(0)]; ok {
			t.Fatal("record contains schema metadata")
		}
		if _, ok := r[codec.Key(1)]; ok {
			t.Fatal("record contains dictionary metadata")
		}
		if r[Keys.Service] != uint64(1) || r[Keys.HttpMethod] != uint64(GET) {
			t.Fatalf("dictionary values not encoded as references: %v", r)
		}
	}
	if err := l.Reconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if peer.checks != 2 || peer.pushes != 1 {
		t.Fatal("matching reconnect should check without pushing")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkStreamTransaction(b *testing.B) {
	connection := &discardConnection{}
	l, err := NewStream(context.Background(), out.ConnectFunc(func(context.Context) (out.Connection, error) { return connection, nil }), Config{Service: codec.Ref(1)})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, tx := l.Begin(context.Background())
		tx.Resource(1).Operation(1).HttpMethod(GET).HttpStatus(200).ResponseBytes(128)
		if err := tx.Finish(); err != nil {
			b.Fatal(err)
		}
	}
}

type discardConnection struct{}

func (*discardConnection) Negotiate(_ context.Context, r codec.Revision) (codec.Revision, error) {
	return r, nil
}
func (*discardConnection) PushSchema(_ context.Context, r codec.Revision, _ []byte) (codec.Revision, error) {
	return r, nil
}
func (*discardConnection) WriteFrame([]byte) error { return nil }
func (*discardConnection) Close() error            { return nil }

func decodeDefinitionFrame(frame []byte, revision codec.Revision) (codec.Definition, error) {
	payload, err := codec.ReadFrame(bytes.NewReader(frame), codec.MaxFramePayload)
	if err != nil {
		return codec.Definition{}, err
	}
	return codec.ParseDefinition(payload, revision)
}

func decodeRecordFrame(frame []byte) (codec.Record, error) {
	payload, err := codec.ReadFrame(bytes.NewReader(frame), codec.MaxFramePayload)
	if err != nil {
		return nil, err
	}
	return codec.Decode(payload, codec.CBOR)
}
