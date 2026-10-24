package main

import (
	"reflect"
	"testing"
	"trie-trunk/go/server/cache"
)

func TestSparseReplayAndIncrementalCatchupMatchCompleteObject(t *testing.T) {
	l := layout()
	for _, textSize := range []int{0, 256, 4096} {
		for _, id := range []int64{1, 42, 9999} {
			var state Object
			for revision := 0; revision < 105; revision++ {
				want := fixture(id, revision, textSize)
				raw := encode(l, want, 10000, false)
				var f cache.Frame
				if err := apply(l, raw, &f, &state); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(state, want) {
					t.Fatalf("replay mismatch at %d", revision)
				}
				// A full PostgreSQL result encoded as PIL must retain every field too.
				var full Object
				if err := apply(l, encode(l, want, 10000, true), &f, &full); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(full, want) {
					t.Fatalf("full PIL mismatch at %d", revision)
				}
				if revision > 0 {
					prior := fixture(id, revision-1, textSize)
					if err := apply(l, raw, &f, &prior); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(prior, want) {
						t.Fatalf("delta mismatch at %d", revision)
					}
				}
			}
		}
	}
}
