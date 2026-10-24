package cache

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func referenceLayouts(t testing.TB) (*Layout, Definition) {
	t.Helper()
	d := Definition{Identity: "payment/transactions", Version: 1 << 20,
		Fields:   []Field{{Name: "event_id", Type: "UUID"}, {Name: "classification", Type: "REF16"}, {Name: "related", Type: "ARRAY<REF16>"}, {Name: "@fields", Type: "CBOR"}},
		Resource: Binding{Format: Format, Service: "payment", Resource: "transactions", Major: 1, Timezone: "UTC", Roles: Roles{Event: "event_id"}}}
	source, err := Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	d.Version++
	d.Resource.Minor++
	d.Fields = []Field{d.Fields[2], d.Fields[0], d.Fields[3], d.Fields[1]}
	d.Resource.References = []ReferenceTranslation{
		{fmt.Sprintf("%x", source.Digest), "classification", map[uint16]uint16{0: 4, 1: 65535, 65535: 0}},
		{fmt.Sprintf("%x", source.Digest), "related", map[uint16]uint16{0: 4, 1: 65535, 65535: 0}},
	}
	return source, d
}

func TestReferenceProjectionExplicitBoundsAndSparseValues(t *testing.T) {
	source, d := referenceLayouts(t)
	target, err := Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	// Compiled metadata cannot be altered through the caller's input maps.
	d.Resource.References[0].Values[1] = 3
	projection, err := CompileProjection(source, target)
	if err != nil {
		t.Fatal(err)
	}
	input := []Value{{State: Absent}, Uint(1, 2), {Offset, []byte{3, 0, 0, 0, 1, 0, 255, 255}}, {State: Absent}}
	mapped, err := projection.Values(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mapped[3].Bytes, []byte{255, 255}) || !bytes.Equal(mapped[0].Bytes, []byte{3, 0, 4, 0, 255, 255, 0, 0}) {
		t.Fatal(mapped)
	}
	if !bytes.Equal(input[2].Bytes, []byte{3, 0, 0, 0, 1, 0, 255, 255}) {
		t.Fatal("source array mutated")
	}
	if err = target.ValidateValues(mapped); err != nil {
		t.Fatal(err)
	}
	if dir := os.Getenv("PIL_REFERENCE_FIXTURE_DIR"); dir != "" {
		// Supply a valid UUIDv7 identity for the cross-language frame fixture.
		mapped[1] = Value{Direct, []byte{1, 0, 0, 0, 0, 0, 0x70, 0, 0x80, 0, 0, 0, 0, 0, 0, 1}}
		raw, err := target.Encode(nil, mapped)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string][]byte{"definition.cbor": target.Bytes, "frame.pil": raw} {
			if err = os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, state := range []State{Absent, Null} {
		input[1], input[2] = Value{State: state}, Value{State: state}
		mapped, err = projection.Values(input)
		if err != nil || mapped[0].State != state || mapped[3].State != state {
			t.Fatal(mapped, err)
		}
	}
	input[1] = Uint(2, 2)
	if _, err = projection.Values(input); err == nil {
		t.Fatal("unmapped scalar accepted")
	}
	input[1] = Value{State: Absent}
	input[2] = Value{Offset, []byte{1, 0, 2, 0}}
	if _, err = projection.Values(input); err == nil {
		t.Fatal("unmapped array element accepted")
	}
	input[2] = Value{Offset, []byte{2, 0, 1, 0}}
	if _, err = projection.Values(input); err == nil {
		t.Fatal("truncated array accepted")
	}
	for _, change := range []func(*Definition){
		func(d *Definition) { d.Resource.References = nil },
		func(d *Definition) { d.Resource.References = d.Resource.References[:1] },
		func(d *Definition) { d.Resource.References[0].Source = fmt.Sprintf("%064x", 123) },
	} {
		_, changed := referenceLayouts(t)
		change(&changed)
		l, err := Compile(changed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = CompileProjection(source, l); err == nil {
			t.Fatal("unbound translation accepted")
		}
	}
	// Ordinary CBOR numbers cannot claim membership in a reference table.
	input[2] = Value{State: Absent}
	input[3] = Value{Offset, []byte{0xa1, 0x6e, 'c', 'l', 'a', 's', 's', 'i', 'f', 'i', 'c', 'a', 't', 'i', 'o', 'n', 1}}
	if _, err = projection.Values(input); err == nil {
		t.Fatal("unbound extension reference promoted")
	}
	input[3].Bytes[len(input[3].Bytes)-1] = 0xf6
	mapped, err = projection.Values(input)
	if err != nil || mapped[3].State != Null {
		t.Fatal("null extension lost its state", err)
	}
}

func TestReferenceDefinitionValidation(t *testing.T) {
	for _, change := range []func(*Definition){
		func(d *Definition) { d.Resource.References[0].Source = "bad" },
		func(d *Definition) { d.Resource.References[0].Field = "event_id" },
		func(d *Definition) { d.Resource.References[0].Values = nil },
		func(d *Definition) { d.Resource.References = append(d.Resource.References, d.Resource.References[0]) },
	} {
		_, d := referenceLayouts(t)
		change(&d)
		if _, err := Compile(d); err == nil {
			t.Fatal("invalid reference map accepted")
		}
	}
}

func BenchmarkReferenceProjection(b *testing.B) {
	source, d := referenceLayouts(b)
	target, err := Compile(d)
	if err != nil {
		b.Fatal(err)
	}
	for _, mode := range []string{"same-map", "translated"} {
		b.Run(mode, func(b *testing.B) {
			dest := source
			if mode == "translated" {
				dest = target
			}
			projection, err := CompileProjection(source, dest)
			if err != nil {
				b.Fatal(err)
			}
			input := []Value{{State: Absent}, Uint(1, 2), {State: Absent}, {State: Absent}}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := projection.Values(input); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
