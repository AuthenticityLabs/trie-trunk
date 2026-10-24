package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"trie-trunk/go/server/cache"
)

// All listeners share one sealing admission limit. Environment changes take
// effect on process restart; resource definitions do not own scheduler limits.
var nativeRuntime struct {
	sync.Once
	memory      uint64
	liveRecords int
	sealer      *cache.SealScheduler
	err         error
}

func configureNativeRuntime() (uint64, *cache.SealScheduler, error) {
	nativeRuntime.Do(func() {
		workers := 1
		if raw, ok := os.LookupEnv("TRUNK_NATIVE_MEMORY_BYTES"); ok {
			nativeRuntime.memory, nativeRuntime.err = strconv.ParseUint(raw, 10, 64)
			if nativeRuntime.err != nil || nativeRuntime.memory == 0 {
				nativeRuntime.err = fmt.Errorf("TRUNK_NATIVE_MEMORY_BYTES must be a positive byte count")
				return
			}
		}
		if raw, ok := os.LookupEnv("TRUNK_LIVE_INDEX_RECORDS"); ok {
			nativeRuntime.liveRecords, nativeRuntime.err = strconv.Atoi(raw)
			if nativeRuntime.err != nil || nativeRuntime.liveRecords < 1 {
				nativeRuntime.err = fmt.Errorf("TRUNK_LIVE_INDEX_RECORDS must be a positive record count")
				return
			}
		}
		if raw, ok := os.LookupEnv("TRUNK_SEAL_WORKERS"); ok {
			workers, nativeRuntime.err = strconv.Atoi(raw)
			if nativeRuntime.err != nil || workers < 1 || workers > 64 {
				nativeRuntime.err = fmt.Errorf("TRUNK_SEAL_WORKERS must be between 1 and 64")
				return
			}
		}
		nativeRuntime.sealer = cache.NewSealScheduler(workers)
	})
	return nativeRuntime.memory, nativeRuntime.sealer, nativeRuntime.err
}

type nativePoolKey struct{}
type nativeEntry struct {
	store  *cache.Endpoint
	digest [32]byte
	tenant string
	refs   int
}
type nativePool struct {
	sync.Mutex
	entries map[string]*nativeEntry
}

// Combined ingest/serve listeners share one writer and staging queue. Separate
// processes still honor the on-disk writer lease.
func openNative(ctx context.Context, root, name, tenant, definition string) (*cache.Endpoint, func() error, error) {
	if name == "" || tenant == "" {
		return nil, nil, fmt.Errorf("ingress requires --cache and --tenant")
	}
	var layout *cache.Layout
	var e error
	if definition != "" {
		var raw []byte
		raw, e = os.ReadFile(definition)
		if e != nil {
			return nil, nil, e
		}
		if strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
			var d cache.Definition
			if e = json.Unmarshal(raw, &d); e == nil {
				layout, e = cache.Compile(d)
			}
		} else {
			layout, e = cache.ParseDefinition(raw)
		}
	}
	if e != nil {
		return nil, nil, e
	}
	var digest [32]byte
	if layout != nil {
		digest = layout.Digest
	}
	memory, sealer, e := configureNativeRuntime()
	if e != nil {
		return nil, nil, e
	}
	config := cache.Config{Root: root, Cache: name, Tenant: tenant, Layout: layout, NativeMemoryBytes: memory, LiveIndexRecords: nativeRuntime.liveRecords, Sealer: sealer}
	pool, _ := ctx.Value(nativePoolKey{}).(*nativePool)
	if pool == nil {
		s, e := cache.OpenEndpoint(config)
		if e != nil {
			return nil, nil, e
		}
		return s, s.Close, nil
	}
	absolute, e := filepath.Abs(filepath.Join(root, name))
	if e != nil {
		return nil, nil, e
	}
	pool.Lock()
	defer pool.Unlock()
	entry := pool.entries[absolute]
	if entry != nil {
		if entry.digest != digest || entry.tenant != tenant {
			return nil, nil, fmt.Errorf("listeners sharing a cache require the same tenant and definition")
		}
	} else {
		s, e := cache.OpenEndpoint(config)
		if e != nil {
			return nil, nil, e
		}
		entry = &nativeEntry{store: s, digest: digest, tenant: tenant}
		pool.entries[absolute] = entry
	}
	entry.refs++
	release := func() error {
		pool.Lock()
		defer pool.Unlock()
		entry.refs--
		if entry.refs == 0 {
			delete(pool.entries, absolute)
			return entry.store.Close()
		}
		return nil
	}
	return entry.store, release, nil
}
