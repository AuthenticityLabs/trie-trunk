package cache

import (
	"errors"
	"fmt"
)

var ErrIndexCapacity = errors.New("index address space exhausted; negotiate the next definition minor")

var ErrStagingFull = errors.New("durable staging capacity reached; retain the event and retry later")

// Limits govern new ingress, not retention of already acknowledged events.
// Zero is unlimited. Byte/record totals span all branches and definition minors
// of this collection; using another branch/minor cannot evade the budget.
type StagingLimits struct {
	MaxBytes     int64 `toml:"max_bytes"`
	MaxRecords   int64 `toml:"max_records"`
	MaxDiskBytes int64 `toml:"max_disk_bytes"`
}

func (l StagingLimits) validate() error {
	if l.MaxBytes < 0 || l.MaxRecords < 0 || l.MaxDiskBytes < 0 {
		return fmt.Errorf("staging limits must be nonnegative")
	}
	return nil
}
func (s *Store) stagingCapacity(incoming int) error {
	l := s.policy.Staging
	if l == nil {
		return nil
	}
	if l.MaxBytes == 0 && l.MaxRecords == 0 && l.MaxDiskBytes == 0 {
		return nil
	}
	stores := []*Store{s}
	if g := s.config.group; g != nil {
		stores = nil
		for _, other := range g.stores {
			if other.collection == s.collection {
				stores = append(stores, other)
			}
		}
	}
	var memory, records, disk int64
	for _, store := range stores {
		for _, b := range store.branches {
			memory += int64(b.pendingBytes)
			records += int64(b.live.count(0))
			if l.MaxDiskBytes > 0 && b.swap != nil {
				info, e := b.swap.Stat()
				if e != nil {
					return e
				}
				disk += info.Size()
			}
		}
	}
	if l.MaxBytes > 0 && int64(incoming) > l.MaxBytes-memory || l.MaxRecords > 0 && records >= l.MaxRecords || l.MaxDiskBytes > 0 && int64(incoming)+8 > l.MaxDiskBytes-disk {
		return ErrStagingFull
	}
	return nil
}

func (s *Store) indexCapacity() uint64 {
	if s.config.indexCapacity != 0 {
		return s.config.indexCapacity
	}
	return IndexCapacity
}
