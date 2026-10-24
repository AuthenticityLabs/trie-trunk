package main

/*
#cgo LDFLAGS: -L${SRCDIR}/build -lboundary_probe
#cgo darwin LDFLAGS: -framework Security -framework CoreFoundation -liconv -lm
#cgo linux LDFLAGS: -ldl -lpthread -lm -lrt -lutil
#include <stdint.h>
#include <stddef.h>
void *session_new(void);
uint8_t *session_buffer(void *, size_t);
size_t session_bytes(void *);
void session_free(void *);
uint64_t payload_sum(const uint8_t *, size_t);
*/
import "C"
import "unsafe"

type nativeSession struct{ ptr unsafe.Pointer }

func newNative() nativeSession { return nativeSession{C.session_new()} }
func (s nativeSession) buffer(n int) []byte {
	p := C.session_buffer(s.ptr, C.size_t(n))
	if p == nil {
		panic("native allocation failed")
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(p)), n)
}
func (s nativeSession) bytes() uint64 { return uint64(C.session_bytes(s.ptr)) }
func (s nativeSession) close()        { C.session_free(s.ptr) }
func checksum(b []byte) uint64 {
	if len(b) == 0 {
		return 0
	}
	return uint64(C.payload_sum((*C.uint8_t)(unsafe.Pointer(&b[0])), C.size_t(len(b))))
}
