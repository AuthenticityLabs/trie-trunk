package search

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"reflect"
	"testing"
	"time"

	"trie-trunk/go/codec"
	"trie-trunk/go/server/store"
)

func TestDictionaryStreamPreservesReferencesAndCatalogScope(t *testing.T) {
	c, _, _, now := fixture(t)
	for i, tap := range []string{"east", "west"} {
		c.Tap = tap
		m, err := store.Open(c)
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		d := testDefinition()
		d.Version = 42
		d.Fields[3] = codec.Field{Name: "level", Enums: map[uint16]string{5: tap}}
		d.Domains = map[string]map[uint16]string{"tier": {1: "PREMIUM"}}
		d.Fields[22] = codec.Field{Name: "items", Array: codec.Schema{1: {Name: "tier", Domain: "tier"}}}
		rev, data, err := d.Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Install(rev, data); err != nil {
			t.Fatal(err)
		}
		for n := 0; n < 2; n++ {
			appendSearchRecord(t, m, rev, now.Add(time.Duration(n*2+i)*time.Millisecond), codec.Record{
				2: uint64(1), 21: map[string]any{"name": "Synthetic Customer"}, 22: []codec.Record{{1: uint64(1)}},
			})
		}
		if err := m.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	q := Config{Root: c.Root, Start: *now, End: now.Add(time.Second), Compact: true, Headers: []string{"duration"}}
	if err := Run(context.Background(), q, &out); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&out)
	catalogs := map[float64]string{}
	active := float64(-1)
	controls, events := 0, 0
	for {
		var value any
		if err := decoder.Decode(&value); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if control, ok := value.(map[string]any); ok {
			controls++
			active = control["schema"].(float64)
			if _, ok := control["map"]; ok {
				t.Fatal("dictionary embedded in events", control)
			}
			if tap, ok := control["tap"].(string); ok {
				if _, exists := catalogs[active]; exists {
					t.Fatal("stream handle redeclared")
				}
				if control["version"] != float64(42) || !reflect.DeepEqual(control["headers"], []any{"duration"}) {
					t.Fatal(control)
				}
				catalogs[active] = tap
			}
			continue
		}
		row, ok := value.([]any)
		if !ok || len(row) != 14 {
			t.Fatal(value)
		}
		if catalogs[active] != []string{"east", "west"}[events%2] {
			t.Fatal("wrong dictionary for event", events, active, catalogs)
		}
		var keys []int
		fields := map[int]any{}
		for i := 0; i < len(row); i += 2 {
			key := int(row[i].(float64))
			keys = append(keys, key)
			fields[key] = row[i+1]
		}
		if !reflect.DeepEqual(keys, []int{8, 3, 14, 2, 12, 21, 22}) {
			t.Fatal("index, lineage, population order", keys)
		}
		if fields[3] != float64(5) || fields[2] != float64(1) || fields[12] != float64(2234) {
			t.Fatal("numeric values expanded", fields)
		}
		if !reflect.DeepEqual(fields[22], []any{map[string]any{"1": float64(1)}}) {
			t.Fatal("nested references expanded", fields[22])
		}
		id, err := base64.StdEncoding.DecodeString(fields[8].(string))
		if err != nil || len(id) != 16 || id[6]>>4 != 7 || id[8]>>6 != 2 {
			t.Fatal("invalid UUIDv7 bytes", id, err)
		}
		events++
	}
	if events != 4 || controls != 4 || len(catalogs) != 2 {
		t.Fatal(events, controls, catalogs)
	}
}
