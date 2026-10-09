//go:build !darwin && !linux

package store

import (
	"fmt"
	"os"
)

func lockRoot(string) (*os.File, error) {
	return nil, fmt.Errorf("storage locking currently supports Linux and macOS")
}
