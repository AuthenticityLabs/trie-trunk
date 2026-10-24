package cache

/*
#include "search_native.h"
*/
import "C"

import "unsafe"

// One aligned native arena per call. No pointer to Go-managed memory is passed
// into Rust or retained there. Dry-run sizing and writing use the same builder.
type searchArena struct {
	bytes []byte
	used  int
}

func searchArray[T any](a *searchArena, n int) []T {
	if n == 0 {
		return nil
	}
	start := (a.used + 7) &^ 7
	a.used = start + n*int(unsafe.Sizeof(*new(T)))
	if a.bytes == nil {
		return nil
	}
	return unsafe.Slice((*T)(unsafe.Pointer(&a.bytes[start])), n)
}
func searchPtr[T any](s []T) *T {
	if len(s) == 0 {
		return nil
	}
	return &s[0]
}
func (a *searchArena) text(s string) C.TTText {
	b := searchArray[byte](a, len(s))
	copy(b, s)
	return C.TTText{ptr: (*C.uint8_t)(unsafe.Pointer(searchPtr(b))), len: C.size_t(len(s))}
}
func (a *searchArena) texts(s []string) C.TTTexts {
	out := searchArray[C.TTText](a, len(s))
	for i, v := range s {
		p := a.text(v)
		if out != nil {
			out[i] = p
		}
	}
	return C.TTTexts{ptr: searchPtr(out), len: C.size_t(len(s))}
}
func (a *searchArena) u64(s []uint64) C.TTU64s {
	out := searchArray[C.uint64_t](a, len(s))
	if out != nil {
		for i, v := range s {
			out[i] = C.uint64_t(v)
		}
	}
	return C.TTU64s{ptr: searchPtr(out), len: C.size_t(len(s))}
}
func (a *searchArena) i64(s []int64) C.TTI64s {
	out := searchArray[C.int64_t](a, len(s))
	if out != nil {
		for i, v := range s {
			out[i] = C.int64_t(v)
		}
	}
	return C.TTI64s{ptr: searchPtr(out), len: C.size_t(len(s))}
}
func (a *searchArena) f64(s []float64) C.TTF64s {
	out := searchArray[C.double](a, len(s))
	if out != nil {
		for i, v := range s {
			out[i] = C.double(v)
		}
	}
	return C.TTF64s{ptr: searchPtr(out), len: C.size_t(len(s))}
}
func (a *searchArena) bools(s []bool) C.TTBytes {
	out := searchArray[C.uint8_t](a, len(s))
	if out != nil {
		for i, v := range s {
			if v {
				out[i] = 1
			} else {
				out[i] = 0
			}
		}
	}
	return C.TTBytes{ptr: searchPtr(out), len: C.size_t(len(s))}
}
func (a *searchArena) request(r searchRequest) *C.TTRequest {
	out := searchArray[C.TTRequest](a, 1)
	native := C.TTRequest{op: a.text(r.Op), path: a.text(r.Path), binding: a.text(r.Binding), text: a.text(r.Text), mode: a.text(r.Mode), base: C.uint64_t(r.Base), tail: C.uint64_t(r.Tail), first_ref: C.uint64_t(r.FirstRef), fields: a.texts(r.Fields), ids: a.texts(r.IDs)}
	ranges := searchArray[C.TTRange](a, len(r.Ranges))
	if ranges != nil {
		for i, v := range r.Ranges {
			ranges[i] = C.TTRange{start: C.uint64_t(v[0]), end: C.uint64_t(v[1])}
		}
	}
	native.ranges = C.TTRanges{ptr: searchPtr(ranges), len: C.size_t(len(r.Ranges))}
	filters := searchArray[C.TTFilter](a, len(r.Filters))
	for i, v := range r.Filters {
		p := C.TTFilter{k: C.uint64_t(v.K), kind: a.text(v.Kind), op: a.text(v.Op), value: a.text(v.Value)}
		if v.Code != nil {
			p.code = C.uint64_t(*v.Code)
			p.has_code = 1
		}
		if filters != nil {
			filters[i] = p
		}
	}
	native.filters = C.TTFilters{ptr: searchPtr(filters), len: C.size_t(len(r.Filters))}
	docs := searchArray[C.TTDoc](a, len(r.Docs))
	for i, v := range r.Docs {
		p := C.TTDoc{id: a.text(v.ID), row: C.uint64_t(v.Row), source_row: C.uint64_t(v.SourceRow)}
		cells := searchArray[C.TTCell](a, len(v.Cells))
		for j, c := range v.Cells {
			x := C.TTCell{k: C.uint64_t(c.K), state: C.uint64_t(c.State), t: a.texts(c.T), c: a.texts(c.C), e: a.texts(c.E), u: a.u64(c.U), i: a.i64(c.I), f: a.f64(c.F), b: a.bools(c.B)}
			if cells != nil {
				cells[j] = x
			}
		}
		p.cells = C.TTCells{ptr: searchPtr(cells), len: C.size_t(len(v.Cells))}
		if docs != nil {
			docs[i] = p
		}
	}
	native.docs = C.TTDocs{ptr: searchPtr(docs), len: C.size_t(len(r.Docs))}
	if out != nil {
		out[0] = native
	}
	return searchPtr(out)
}
