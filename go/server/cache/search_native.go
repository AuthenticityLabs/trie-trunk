package cache

/*
#cgo LDFLAGS: -L${SRCDIR}/../../../rust/target/release -ltrie_trunk_search
#cgo darwin LDFLAGS: -framework Security -framework CoreFoundation -liconv -lm
#cgo linux LDFLAGS: -ldl -lpthread -lm -lrt -lutil
#include <stdlib.h>
#include "search_native.h"
*/
import "C"

import (
	"context"
	"errors"
	"unsafe"
)

func nativeText(s C.TTText) string {
	if s.len == 0 {
		return ""
	}
	return string(unsafe.Slice((*byte)(unsafe.Pointer(s.ptr)), int(s.len)))
}

func searchCall(ctx context.Context, r searchRequest) (searchResponse, error) {
	if err := ctx.Err(); err != nil {
		return searchResponse{}, err
	}
	var sizing searchArena
	sizing.request(r)
	if sizing.used > 64<<20 {
		return searchResponse{}, errors.New("native search request too large")
	}
	// Go assigns pointer-bearing C structs into this arena. Cgo requires the
	// destination to be zeroed first: stale malloc bytes can resemble Go pointers
	// and be shaded by the GC write barrier (see cmd/cgo, Passing pointers).
	mem := C.calloc(1, C.size_t(sizing.used))
	if mem == nil {
		return searchResponse{}, errors.New("native search allocation failed")
	}
	defer C.free(mem)
	arena := searchArena{bytes: unsafe.Slice((*byte)(mem), sizing.used)}
	req := arena.request(r)
	var cancel *C.TTCancel
	if ctx.Done() != nil {
		cancel = C.trunk_search_cancel_new()
		// Registers a callback; no waiting goroutine for each successful request.
		done := make(chan struct{})
		stop := context.AfterFunc(ctx, func() { C.trunk_search_cancel_set(cancel); close(done) })
		defer func() {
			if !stop() {
				<-done
			}
			C.trunk_search_cancel_free(cancel)
		}()
	}
	result := C.trunk_search_execute(req, cancel)
	if result == nil {
		return searchResponse{}, errors.New("native search returned no result")
	}
	defer C.trunk_search_result_free(result)
	if err := ctx.Err(); err != nil {
		return searchResponse{}, err
	}
	if result.error.len != 0 {
		return searchResponse{}, errors.New(nativeText(result.error))
	}
	reply := searchResponse{Tail: uint64(result.tail)}
	if result.rows.len != 0 {
		rows := unsafe.Slice(result.rows.ptr, int(result.rows.len))
		reply.Rows = make([]uint64, len(rows))
		for i, v := range rows {
			reply.Rows[i] = uint64(v)
		}
	}
	if result.ids.len != 0 {
		ids := unsafe.Slice(result.ids.ptr, int(result.ids.len))
		reply.IDs = make([]string, len(ids))
		for i, v := range ids {
			reply.IDs[i] = nativeText(v)
		}
	}
	return reply, nil
}
