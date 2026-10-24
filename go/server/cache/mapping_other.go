//go:build !darwin && !linux

package cache

import "fmt"

func (b *branchStore) mapIndex() error   { return fmt.Errorf("index mapping requires macOS or Linux") }
func (b *branchStore) unmapIndex() error { return nil }
func (b *branchStore) snapshotIndex() ([]byte, func(), error) {
	return nil, nil, fmt.Errorf("index mapping requires macOS or Linux")
}
