package store

import (
	"sync"
	"testing"

	"trie-trunk/go/codec"
)

func TestRegisterAssignsAndReusesImmutableDefinition(t *testing.T) {
	config, installed, data, _ := fixture(t)
	m := mustOpen(t, config, installed, data)
	d := testDefinition()
	d.Dictionary = "jsonl fallback"
	var revisions [8]codec.Revision
	var wg sync.WaitGroup
	for i := range revisions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := m.Register(d)
			if err != nil {
				t.Error(err)
				return
			}
			revisions[i] = r
		}()
	}
	wg.Wait()
	for _, r := range revisions {
		if r != revisions[0] || r.Version != 1<<20 {
			t.Fatal(revisions)
		}
	}

	d.Fields[8] = codec.Field{Name: "changed"}
	if m.schemas[revisions[0].Digest].definition.Fields[8].Name != "id" {
		t.Fatal("retained caller-owned map")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	m, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	d = testDefinition()
	d.Dictionary = "jsonl fallback"
	d.Version = 999
	r, err := m.Register(d)
	if err != nil || r != revisions[0] || len(m.schemas) != 2 {
		t.Fatal(r, err)
	}
}
