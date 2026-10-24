package cache

import (
	"bytes"
	"math"
	"testing"
)

func TestProjectionCompatibilityAndSparseValues(t *testing.T) {
	d := Definition{Identity: "payment/transactions", Version: 1 << 20, PIL: 1, AdaptiveIntegers: "16", Fields: []Field{{Name: "event_id", Type: "UUID"}, {Name: "amount", Type: "I32"}, {Name: "@fields", Type: "CBOR"}}, Resource: Binding{Format: Format, Service: "payment", Resource: "transactions", Major: 1, Timezone: "UTC", Roles: Roles{Event: "event_id"}}}
	source, e := Compile(d)
	if e != nil {
		t.Fatal(e)
	}
	d.Version++
	d.Resource.Minor++
	d.PIL = 0
	d.AdaptiveIntegers = ""
	d.Fields = []Field{{Name: "amount", Type: "I32"}, {Name: "event_id", Type: "UUID"}, {Name: "samples", Type: "ARRAY<U16>"}, {Name: "@fields", Type: "CBOR"}}
	target, e := Compile(d)
	if e != nil {
		t.Fatal(e)
	}
	p, e := CompileProjection(source, target)
	if e != nil {
		t.Fatal(e)
	}
	// An adaptive signed byte must become a signed fixed-width value, not 255.
	mapped, e := p.Values([]Value{{State: Absent}, {State: Direct, Bytes: []byte{255}}, {State: Offset, Bytes: []byte{0xa1, 0x67, 's', 'a', 'm', 'p', 'l', 'e', 's', 0x82, 1, 2}}})
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(mapped[0].Bytes, []byte{255, 255, 255, 255}) || mapped[1].State != Absent || !bytes.Equal(mapped[2].Bytes, []byte{2, 0, 1, 0, 2, 0}) {
		t.Fatal(mapped)
	}
	if e = target.ValidateValues(mapped); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*Definition){
		func(d *Definition) { d.Version = 1 << 20; d.Resource.Minor = 0 },
		func(d *Definition) { d.Resource.Major = 2; d.Version = 2<<20 | 1 },
		func(d *Definition) { d.Fields[0].Type = "U32" },
		func(d *Definition) { d.Fields = d.Fields[:3] },
	} {
		copy := d
		copy.Fields = append([]Field(nil), d.Fields...)
		change(&copy)
		l, e := Compile(copy)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = CompileProjection(source, l); e == nil {
			t.Fatal("accepted incompatible definition")
		}
	}
}

func TestExtensionPreservesEmptyBytesAndNonfiniteFloat(t *testing.T) {
	b, e := fieldCBOR(Field{Type: "BYTES"}, Value{State: Offset})
	if e != nil || !bytes.Equal(b, []byte{0x40}) {
		t.Fatal("empty bytes became null", b, e)
	}
	b, e = fieldCBOR(Field{Type: "F64"}, Value{State: Direct, Bytes: []byte{0, 0, 0, 0, 0, 0, 0xf0, 0x7f}})
	if e != nil {
		t.Fatal(e)
	}
	var value float64
	if e = decoder.Unmarshal(b, &value); e != nil || !math.IsInf(value, 1) {
		t.Fatal("infinity changed type", b, e)
	}
}
