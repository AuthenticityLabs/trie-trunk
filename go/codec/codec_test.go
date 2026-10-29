package codec

import (
	"encoding/json"
	pil "github.com/AuthenticityLabs/pill-packer/go/codec"
	"testing"
)

func TestSymbolWireBoundaries(t *testing.T) {
	for _, id := range []uint16{1, 23, 24, 255, 256, 65535} {
		got, err := MarshalCompact(Ref(id), PIL)
		var n uint64
		if err != nil || pil.UnmarshalObject(got, &n) != nil || n != uint64(id) {
			t.Fatal(id, err)
		}
	}
	for _, format := range []Format{CompactJSON, PIL} {
		data, err := Encode(Record{0: Ref(2), 1: Text("2")}, nil, nil, format)
		if err != nil {
			t.Fatal(err)
		}
		r, err := Decode(data, format)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := r[1].(string); !ok {
			t.Fatalf("lost literal type: %#v", r)
		}
	}
}

func TestDictionaryCopiesAndUnknowns(t *testing.T) {
	if _, err := NewDictionary("invalid/v1", map[string]map[uint16]string{"error_type": {1: "CUSTOM"}}); err == nil {
		t.Fatal("allowed redefinition of reserved type")
	}
	domains := map[string]map[uint16]string{"service": {1: "customers"}}
	d, err := NewDictionary("customers/v1", domains)
	if err != nil {
		t.Fatal(err)
	}
	domains["service"][1] = "changed"
	if d.Resolve("service", Ref(1)) != "customers" {
		t.Fatal("dictionary retained mutable map")
	}
	r := Expand(Record{1: Ref(1), 2: Ref(456), 77: "future"}, Schema{1: {Name: "service", Domain: "service"}, 2: {Name: "future_enum", Domain: "service"}}, d)
	if r["service"] != "customers" || r["future_enum"] != uint16(456) || r["77"] != "future" {
		t.Fatalf("lost dictionary values: %#v", r)
	}
}

func TestValueCopiesAndRedaction(t *testing.T) {
	input := map[string]Value{"Password": String("secret"), "nested": List(String("abcdefghijkl"))}
	v := Object(input)
	input["Password"] = String("changed")
	safe := v.Sanitize(8, 4, 4, map[string]bool{"password": true})
	b, err := json.Marshal(safe)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"Password":"*******","nested":["abcdefgh"]}` {
		t.Fatalf("unexpected sanitized value: %s", b)
	}
	if got := LimitText("ééé", 3); got != "é" {
		t.Fatalf("split UTF-8: %q", got)
	}
}

func TestDecodeRejectsMalformed(t *testing.T) {
	for _, data := range [][]byte{[]byte(`null`), []byte(`{"1":2} {"1":3}`), []byte(`[]`)} {
		if _, err := Decode(data, CompactJSON); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
	if _, err := Decode([]byte{0xa2, 1, 2, 1, 3}, CBOR); err == nil {
		t.Fatal("accepted duplicate CBOR keys")
	}
}

func TestCBOROutputIsUnsupported(t *testing.T) {
	if _, err := MarshalCompact(Record{1: "value"}, CBOR); err == nil {
		t.Fatal("CBOR output is supported")
	}
	if _, err := Encode(Record{1: "value"}, nil, nil, CBOR); err == nil {
		t.Fatal("CBOR output is supported")
	}
}
