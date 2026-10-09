//go:build darwin || linux

package store

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func lockRoot(root string) (*os.File, error) {
	f, e := os.OpenFile(filepath.Join(root, ".writer.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, fmt.Errorf("store already owned by another writer: %w", e)
	}
	return f, nil
}
