package cache

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
)

const headerSize = 72

var order = binary.LittleEndian
var checksum = crc32.MakeTable(crc32.Castagnoli)

func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func replace(path string, data []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".publish-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(data)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	return syncDir(filepath.Dir(path))
}
func openFile(path, magic string, digest [32]byte, generation [16]byte, rowWidth int) (*os.File, error) {
	f, e := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if e != nil {
		return nil, e
	}
	fail := func(e error) (*os.File, error) { f.Close(); return nil, e }
	var h [headerSize]byte
	copy(h[:8], magic)
	copy(h[8:40], digest[:])
	copy(h[40:56], generation[:])
	order.PutUint32(h[56:60], uint32(rowWidth))
	order.PutUint32(h[60:], Format)
	stat, e := f.Stat()
	if e != nil {
		return fail(e)
	}
	if stat.Size() == 0 {
		if _, e = f.Write(h[:]); e != nil {
			return fail(e)
		}
		if e = f.Sync(); e != nil {
			return fail(e)
		}
	} else {
		var actual [headerSize]byte
		if _, e = f.ReadAt(actual[:], 0); e != nil {
			return fail(e)
		}
		if !bytes.Equal(actual[:64], h[:64]) {
			return fail(fmt.Errorf("file binding mismatch: %s", path))
		}
	}
	return f, nil
}

// A .journal commit is the publication point for previously synced row/pods bytes.
// An incomplete last journal record is discarded; a complete corrupt one fails.
func records(f *os.File, visit func([]byte) error) error {
	return boundedRecords(f, MaxFrame, visit)
}
func boundedRecords(f *os.File, limit int, visit func([]byte) error) error {
	return visitRecords(f, limit, nil, func(raw []byte, _ *nativeBuffer) error { return visit(raw) })
}
func visitRecords(f *os.File, limit int, memory *nativeMemory, visit func([]byte, *nativeBuffer) error) error {
	var h [8]byte
	var stream nativeIngress
	if memory != nil {
		stream.memory = memory
		defer stream.close()
	}
	at := int64(headerSize)
	for {
		n, e := f.ReadAt(h[:], at)
		if e == io.EOF && n == 0 {
			return nil
		}
		if e != nil {
			if e == io.EOF {
				return f.Truncate(at)
			}
			return e
		}
		size := int(order.Uint32(h[:4]))
		if size <= 0 || size > limit {
			return fmt.Errorf("invalid journal record size")
		}
		var b []byte
		var buffer *nativeBuffer
		if memory != nil {
			frame, err := stream.allocate(size)
			if err != nil {
				return err
			}
			b, buffer = frame.raw, frame.buffer
		} else {
			b = make([]byte, size)
		}
		release := func() {
			if memory != nil {
				stream.staged()
			}
		}
		if _, e = f.ReadAt(b, at+8); e != nil {
			release()
			if e == io.EOF {
				return f.Truncate(at)
			}
			return e
		}
		if crc32.Checksum(b, checksum) != order.Uint32(h[4:]) {
			release()
			return fmt.Errorf("journal checksum mismatch")
		}
		e = visit(b, buffer)
		release()
		if e != nil {
			return e
		}
		at += 8 + int64(size)
	}
}
func appendRecord(f *os.File, b []byte) error { return appendBoundedRecord(f, b, MaxFrame) }
func appendBoundedRecord(f *os.File, b []byte, limit int) error {
	if len(b) == 0 || len(b) > limit {
		return fmt.Errorf("invalid journal record size")
	}
	var h [8]byte
	order.PutUint32(h[:4], uint32(len(b)))
	order.PutUint32(h[4:], crc32.Checksum(b, checksum))
	if _, e := f.Seek(0, io.SeekEnd); e != nil {
		return e
	}
	if _, e := f.Write(h[:]); e != nil {
		return e
	}
	_, e := f.Write(b)
	return e
}
func readBytes(f *os.File, start, end uint64) ([]byte, error) {
	if end < start || end-start > MaxFrame*8 {
		return nil, fmt.Errorf("invalid byte range")
	}
	b := make([]byte, int(end-start))
	_, e := f.ReadAt(b, int64(start))
	return b, e
}
func ensureMap(path string, b []byte) error {
	old, e := os.ReadFile(path)
	if e == nil {
		if !bytes.Equal(old, b) {
			return fmt.Errorf("immutable definition conflict")
		}
		return nil
	}
	if !os.IsNotExist(e) {
		return e
	}
	return replace(path, b)
}

// retired_bytes counts removed payload bytes, excluding the fixed file header.
func retiredBytes(f *os.File) (uint64, error) {
	var b [8]byte
	_, e := f.ReadAt(b[:], 64)
	return order.Uint64(b[:]), e
}
func setRetiredBytes(f *os.File, n uint64) error {
	var b [8]byte
	order.PutUint64(b[:], n)
	_, e := f.WriteAt(b[:], 64)
	return e
}
