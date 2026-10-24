package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"
	"trie-trunk/go/server/cache"
)

func TestSeedPersistsReplayPolicy(t *testing.T) {
	root := filepath.Join(t.TempDir(), "caches")
	end := time.Now().Add(-20 * time.Minute).Truncate(time.Second)
	r, e := seed(root, 300, 1, end)
	if e != nil {
		t.Fatal(e)
	}
	if r.Events != 300 || len(r.Resources) != 5 {
		t.Fatal(r)
	}
	endpoint, e := cache.OpenEndpoint(cache.Config{Root: root, Cache: "demo", Tenant: "local"})
	if e != nil {
		t.Fatal(e)
	}
	defer endpoint.Close()
	for _, s := range scenarios {
		count := 0
		e = endpoint.History(context.Background(), s.service+"/"+s.resource, 1, objectID(5), r.From, r.To, func(cache.EventReference, []byte) error { count++; return nil })
		if e != nil || count != 1 {
			t.Fatalf("%s history=%d: %v", s.service, count, e)
		}
	}
	if _, e = seed(root, 300, 1, end); e == nil {
		t.Fatal("overwrote existing dataset")
	}
}
