//go:build darwin || linux

package cache

import (
	"fmt"
	"golang.org/x/sys/unix"
)

func (b *branchStore) mapIndex() error {
	stat, e := b.idx.Stat()
	if e != nil {
		return e
	}
	size := stat.Size()
	if size < headerSize || int64(int(size)) != size {
		return fmt.Errorf("index mapping too large")
	}
	mapped, e := unix.Mmap(int(b.idx.Fd()), 0, int(size), unix.PROT_READ, unix.MAP_SHARED)
	if e != nil {
		return e
	}
	if e = b.unmapIndex(); e != nil {
		unix.Munmap(mapped)
		return e
	}
	b.mapping = mapped
	b.rows = mapped[headerSize:]
	return nil
}
func (b *branchStore) unmapIndex() error {
	if b.mapping == nil {
		return nil
	}
	e := unix.Munmap(b.mapping)
	b.mapping = nil
	b.rows = nil
	return e
}

// A separate mapping owns its lifetime. Borrowing b.rows across an unlocked
// callback is unsafe: an append remaps it and retention unmaps the old branch.
func (b *branchStore) snapshotIndex() ([]byte, func(), error) {
	if len(b.rows) == 0 {
		return nil, func() {}, nil
	}
	mapped, err := unix.Mmap(int(b.idx.Fd()), 0, headerSize+len(b.rows), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return nil, nil, err
	}
	return mapped[headerSize:], func() { _ = unix.Munmap(mapped) }, nil
}
