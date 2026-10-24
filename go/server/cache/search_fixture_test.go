package cache

// CBOR belongs to cross-language benchmark fixtures, not the native runtime ABI.
import (
	"github.com/fxamacker/cbor/v2"
	"reflect"
	"testing"
)

type fixtureSearchCell struct {
	State uint64    `cbor:"state"`
	K     int       `cbor:"k"`
	T     []string  `cbor:"t,omitempty"`
	E     []string  `cbor:"e,omitempty"`
	C     []string  `cbor:"c,omitempty"`
	U     []uint64  `cbor:"u,omitempty"`
	I     []int64   `cbor:"i,omitempty"`
	F     []float64 `cbor:"f,omitempty"`
	B     []bool    `cbor:"b,omitempty"`
}
type fixtureSearchDoc struct {
	SourceRow uint64       `cbor:"source_row"`
	ID        string       `cbor:"id,omitempty"`
	Row       uint64       `cbor:"row"`
	Cells     []searchCell `cbor:"cells"`
}

func (c searchCell) MarshalCBOR() ([]byte, error) { return cbor.Marshal(fixtureSearchCell(c)) }
func (c *searchCell) UnmarshalCBOR(b []byte) error {
	var v fixtureSearchCell
	if err := cbor.Unmarshal(b, &v); err != nil {
		return err
	}
	*c = searchCell(v)
	return nil
}
func (d searchDoc) MarshalCBOR() ([]byte, error) { return cbor.Marshal(fixtureSearchDoc(d)) }
func (d *searchDoc) UnmarshalCBOR(b []byte) error {
	var v fixtureSearchDoc
	if err := cbor.Unmarshal(b, &v); err != nil {
		return err
	}
	*d = searchDoc(v)
	return nil
}
func TestSearchFixtureEncoding(t *testing.T) {
	want := searchDoc{SourceRow: 42, ID: "event", Row: 7, Cells: []searchCell{{State: 2, K: 3, T: []string{"text"}, E: []string{"exact"}, C: []string{"code"}, U: []uint64{1 << 63}, I: []int64{-4}, F: []float64{1.5}, B: []bool{true}}}}
	b, err := cbor.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]cbor.RawMessage
	if err = cbor.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"source_row", "id", "row", "cells"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("missing fixture field %s", key)
		}
	}
	var got searchDoc
	if err = cbor.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fixture mismatch: %#v", got)
	}
}
