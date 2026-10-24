//go:build stagedobserver

package main

import "trie-trunk/go/server/cache"

// Present only in the benchmark build overlay, never in a deployed store.
func setStagedObserver(f func([][]byte) error) error {
	cache.BenchmarkBeforeSwapSync = f
	return nil
}
