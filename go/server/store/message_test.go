package store

import (
	"testing"
	"time"
	"trie-trunk/go/codec"
)

func TestDictionaryMessageRemainsInPopulation(t *testing.T) {
	d := testDefinition()
	d.Fields[15] = codec.Field{Name: "message", Enums: map[uint16]string{1: "Successful transaction.", 2: "Transaction failed."}}
	revision, _, err := d.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	s, err := compile(d, revision)
	if err != nil {
		t.Fatal(err)
	}
	id, err := codec.NewUUIDv7(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Dictionary-backed messages must stay dynamic, just like literal text.
	for _, message := range []any{uint64(1), uint64(2), "A literal diagnostic"} {
		data, err := codec.MarshalCompact(codec.Record{
			8: id, 12: uint64(100),
			3: uint64(3), 15: message,
		}, codec.CBOR)
		if err != nil {
			t.Fatal(err)
		}
		event, err := s.decode(data)
		if err != nil {
			t.Fatal(err)
		}
		if len(event.Path) != 1 || event.Path[0].Key != 3 {
			t.Fatalf("message %v changed the trie lineage: %+v", message, event.Path)
		}
		var population map[codec.Key]any
		if err := decoder.Unmarshal(event.Dynamic, &population); err != nil {
			t.Fatal(err)
		}
		if population[15] != message {
			t.Fatalf("population message = %v, want %v", population[15], message)
		}
	}
}
