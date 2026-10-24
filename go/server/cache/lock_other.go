//go:build !darwin && !linux

package cache

import (
	"fmt"
	"os"
)

func lockRoot(string) (*os.File, error) {
	return nil, fmt.Errorf("cache locking requires macOS or Linux")
}
