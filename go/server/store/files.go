package store

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"

	"trie-trunk/go/codec"
)

const fileHeaderSize = 8
const storageFormat uint32 = 2
const maxBlockSize = 16 << 20

var order = binary.BigEndian
var crcTable = crc32.MakeTable(crc32.Castagnoli)

func readMap(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() < 1 || info.Size() > codec.MaxFramePayload {
		return nil, codec.ErrFrameSize
	}
	data := make([]byte, int(info.Size()))
	_, err = io.ReadFull(file, data)
	return data, err
}

func openLog(path, magic string) (*os.File, error) {
	return openLogVersion(path, magic, storageFormat)
}
func openLogVersion(path, magic string, version uint32) (*os.File, error) {
	f, e := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if e != nil {
		return nil, e
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
		}
	}()
	var h [fileHeaderSize]byte
	copy(h[:4], magic)
	order.PutUint32(h[4:8], version)
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if st.Size() == 0 {
		if _, e = f.Write(h[:]); e != nil {
			return nil, e
		}
		if e = f.Sync(); e != nil {
			return nil, e
		}
	} else {
		var got [fileHeaderSize]byte
		if _, e = io.ReadFull(f, got[:]); e != nil {
			return nil, e
		}
		if got != h {
			return nil, fmt.Errorf("file header mismatch: %s", path)
		}
	}
	ok = true
	return f, nil
}

func scan(f *os.File, visit func(int64, int64, []byte) error) error {
	if _, e := f.Seek(fileHeaderSize, io.SeekStart); e != nil {
		return e
	}
	for {
		start, e := f.Seek(0, io.SeekCurrent)
		if e != nil {
			return e
		}
		var h [8]byte
		_, e = io.ReadFull(f, h[:])
		if e == io.EOF {
			return nil
		}
		if e == io.ErrUnexpectedEOF {
			return f.Truncate(start)
		}
		if e != nil {
			return e
		}
		n := order.Uint32(h[:4])
		if n == 0 || n > maxBlockSize {
			return fmt.Errorf("invalid disk block length")
		}
		b := make([]byte, int(n))
		_, e = io.ReadFull(f, b)
		if e == io.EOF || e == io.ErrUnexpectedEOF {
			return f.Truncate(start)
		}
		if e != nil {
			return e
		}
		if crc32.Checksum(b, crcTable) != order.Uint32(h[4:]) {
			return fmt.Errorf("checksum failure in %s at %d", f.Name(), start)
		}
		if e = visit(start, start+8+int64(n), b); e != nil {
			return e
		}
	}
}

func appendBlock(f *os.File, b []byte) (int64, error) {
	if len(b) == 0 || len(b) > maxBlockSize {
		return 0, ErrLimit
	}
	start, e := f.Seek(0, io.SeekEnd)
	if e != nil {
		return 0, e
	}
	var h [8]byte
	order.PutUint32(h[:4], uint32(len(b)))
	order.PutUint32(h[4:], crc32.Checksum(b, crcTable))
	if _, e = f.Write(h[:]); e == nil {
		_, e = f.Write(b)
	}
	if e != nil {
		_ = f.Truncate(start)
		return 0, e
	}
	return start + 8 + int64(len(b)), nil
}

func syncDirectory(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}

func mkdirDurable(path string) error {
	if _, e := os.Stat(path); e == nil {
		return nil
	} else if !os.IsNotExist(e) {
		return e
	}
	parent := filepath.Dir(path)
	if parent != path {
		if e := mkdirDurable(parent); e != nil {
			return e
		}
	}
	if e := os.Mkdir(path, 0700); e != nil && !os.IsExist(e) {
		return e
	}
	return syncDirectory(parent)
}

func installMap(dir string, data []byte) error { return installNamedMap(dir, "log.map", data) }
func installNamedMap(dir, filename string, data []byte) error {
	if e := mkdirDurable(dir); e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".map-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(data); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(name, filepath.Join(dir, filename)); e != nil {
		return e
	}
	return syncDirectory(dir)
}
