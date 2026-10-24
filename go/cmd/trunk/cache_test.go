package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"trie-trunk/go/server/cache"
)

func TestCacheCLI(t *testing.T) {
	root := t.TempDir()
	d := cache.Definition{Identity: "payment/transactions", Version: 1 << 20, Fields: []cache.Field{{Name: "event_id", Type: "UUID"}, {Name: "state", Type: "STRING"}}, Resource: cache.Binding{Format: cache.Format, Service: "payment", Resource: "transactions", Major: 1, Timezone: "UTC", Roles: cache.Roles{Event: "event_id"}}}
	l, e := cache.Compile(d)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(root, "definition.json")
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	common := []string{"--dir", filepath.Join(root, "caches"), "--cache", "test", "--tenant", "owner", "--definition", path}
	var output bytes.Buffer
	invoke := func(command string, input []byte, extra ...string) error {
		args := append([]string{command}, common...)
		args = append(args, extra...)
		return runCache(context.Background(), args, bytes.NewReader(input), &output)
	}
	if e = invoke("init", nil); e != nil {
		t.Fatal(e)
	}
	id := []byte{1, 0, 0, 0, 0, 0, 0x70, 0, 0x80, 0, 0, 0, 0, 0, 0, 1}
	frame, e := l.Encode(nil, []cache.Value{{State: cache.Direct, Bytes: id}, cache.Text("authorized")})
	if e != nil {
		t.Fatal(e)
	}
	if e = invoke("ingest", frame); e != nil {
		t.Fatal(e)
	}
	if e = invoke("search", nil, "--from", "1970-01-01T00:00:00Z", "--to", "2050-01-01T00:00:00Z"); e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(output.Bytes(), frame) {
		t.Fatal("CLI PIL round trip differs")
	}
	// Administrative promotion is durable and is separate from registration.
	d.Version++
	d.Resource.Minor++
	raw, e = json.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	output.Reset()
	if e = invoke("prefer", nil); e != nil {
		t.Fatal(e)
	}
	if !bytes.Contains(output.Bytes(), []byte("minor 1:")) {
		t.Fatal(output.String())
	}
	output.Reset()
	if e = runCache(context.Background(), []string{"definitions", "--dir", filepath.Join(root, "caches"), "--cache", "test", "--tenant", "owner"}, bytes.NewReader(nil), &output); e != nil {
		t.Fatal(e)
	}
	var definitions []cache.DefinitionStatus
	if e = json.Unmarshal(output.Bytes(), &definitions); e != nil {
		t.Fatal(e)
	}
	if len(definitions) != 2 || definitions[0].Preferred || !definitions[1].Preferred {
		t.Fatal(definitions)
	}
	output.Reset()
	if e = runCache(context.Background(), []string{"cleanup", "--dir", filepath.Join(root, "caches"), "--cache", "test", "--tenant", "owner"}, bytes.NewReader(nil), &output); e != nil {
		t.Fatal(e)
	}
	var cleanup cache.CleanupReport
	if e = json.Unmarshal(output.Bytes(), &cleanup); e != nil || len(cleanup.Files) != 0 {
		t.Fatal(cleanup, e)
	}
	// An offline administrative command cannot bypass an active writer lease.
	p, e := cache.OpenEndpoint(cache.Config{Root: filepath.Join(root, "caches"), Cache: "test", Tenant: "owner"})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	if e = runCache(context.Background(), []string{"cleanup", "--dir", filepath.Join(root, "caches"), "--cache", "test", "--tenant", "owner", "--apply"}, bytes.NewReader(nil), &output); e == nil {
		t.Fatal("cleanup bypassed active writer")
	}
	if e = invoke("prefer", nil); e == nil {
		t.Fatal("promotion bypassed active writer")
	}
}
