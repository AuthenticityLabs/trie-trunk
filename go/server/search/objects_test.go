package search

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"trie-trunk/go/codec"
	"trie-trunk/go/server/store"
)

func TestNamedObjectsShareVersionAndPILRetainsTap(t *testing.T) {
	c, _, _, now := fixture(t)
	c.Tap = "demo"
	m, err := store.Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for _, name := range []string{"orders", "inventory"} {
		d := codec.Definition{Object: name, Version: 1 << 20, Timezone: "UTC", Storage: &codec.StoragePolicy{Format: 3, Boundary: "state", Bucket: "hour"}, Fields: codec.Schema{0: {Name: "id", Encoding: "uuidv7"}, 1: {Name: "state", Enums: map[uint16]string{1: name}}, 2: {Name: "value"}}, Path: []codec.Key{1}}
		rev, data, err := d.Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if err = m.Install(rev, data); err != nil {
			t.Fatal(err)
		}
		id, err := codec.NewUUIDv7(now.Add(-time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		data, err = codec.MarshalCompact(codec.Record{0: id, 1: uint64(1), 2: name}, codec.PIL)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = m.Append(rev, data); err != nil {
			t.Fatal(err)
		}
	}
	if err = m.Flush(); err != nil {
		t.Fatal(err)
	}
	q := Config{Root: c.Root, Tap: "demo", Start: now.Add(-time.Hour), End: *now}
	rows := searchResults(t, q)
	if len(rows) != 2 {
		t.Fatal(rows)
	}
	for _, row := range rows {
		if _, ok := row["duration"]; ok {
			t.Fatal("invented duration", row)
		}
	}
	var native, wire, decoded bytes.Buffer
	if err = Run(context.Background(), q, &native); err != nil {
		t.Fatal(err)
	}
	q.PIL, q.PILV2 = true, true
	if err = Run(context.Background(), q, &wire); err != nil {
		t.Fatal(err)
	}
	wire.WriteByte(0xfe)
	if err = DecodePIL(context.Background(), q, io.NopCloser(bytes.NewReader(wire.Bytes())), &decoded); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(native.Bytes(), decoded.Bytes()) {
		t.Fatal(native.String(), decoded.String())
	}
	q.PIL, q.PILV2 = false, false
	q.Where = []string{`state=="orders"`}
	if rows = searchResults(t, q); len(rows) != 1 || rows[0]["value"] != "orders" {
		t.Fatal(rows)
	}
}
