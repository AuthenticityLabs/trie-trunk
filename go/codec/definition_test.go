package codec

import (
	"testing"
)

func TestDefinitionRevisionAndOwnership(t *testing.T) {
	dictionary, err := NewDictionary("customers/v1", map[string]map[uint16]string{"service": {1: "customers"}})
	if err != nil {
		t.Fatal(err)
	}
	d := Define(2, Schema{2: {Name: "category", Enums: map[uint16]string{1: "SUMMARY"}}, 4: {Name: "service", Domain: "service"}}, dictionary)
	revision, data, err := d.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ParseDefinition(data, revision)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Fields[2].Enums[1] != "SUMMARY" || decoded.Domains["service"][1] != "customers" {
		t.Fatalf("incomplete definition: %+v", decoded)
	}
	again, _, err := decoded.Prepare()
	if err != nil || again != revision {
		t.Fatal("revision is not deterministic")
	}
	d.Domains["service"][1] = "changed"
	changed, _, _ := d.Prepare()
	if changed == revision || changed.Version != revision.Version {
		t.Fatal("catalog edit not detected at same schema version")
	}
	if dictionary.Resolve("service", Ref(1)) != "customers" {
		t.Fatal("definition modified active catalog")
	}
	data[len(data)-1] ^= 1
	if _, err := ParseDefinition(data, revision); err == nil {
		t.Fatal("accepted corrupted schema")
	}
}
