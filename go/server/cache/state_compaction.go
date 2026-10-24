package cache

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

type stateCheckpoint struct {
	started, complete    bool
	count                uint64
	sequence, collection uint64
	last                 []byte
}

func (c *stateCheckpoint) apply(st *objectState, record invalidation) error {
	p := record.Snapshot
	if c.complete || st.sequence != 0 || record.Sequence != 0 || record.Collection || len(record.Objects) > 0 || !p.Paged || p.Unchanged ||
		p.Epoch != st.epoch || p.Collection > p.Sequence || record.SnapshotStart != c.count || len(p.Objects) > 1024 {
		return fmt.Errorf("invalid invalidation checkpoint")
	}
	if c.started && (c.sequence != p.Sequence || c.collection != p.Collection) {
		return fmt.Errorf("mixed invalidation checkpoints")
	}
	c.started = true
	c.sequence = p.Sequence
	c.collection = p.Collection
	for _, entry := range p.Objects {
		if entry.Token == 0 || entry.Token > p.Sequence || c.count > 0 && bytes.Compare(c.last, entry.ID) >= 0 {
			return fmt.Errorf("invalid checkpoint object token")
		}
		st.objects[string(entry.ID)] = entry.Token
		c.last = entry.ID
		c.count++
	}
	if !p.Final && len(p.Objects) == 0 {
		return fmt.Errorf("empty checkpoint page")
	}
	if p.Final {
		c.complete = true
		st.sequence = p.Sequence
		st.collection = p.Collection
	}
	return nil
}
func (st *objectState) compactionThreshold() int64 {
	live := int64(256) + int64(len(st.objects))*64 + st.identityBytes
	if live > (1<<63-1)/4 {
		return 1<<63 - 1
	}
	threshold := 4 * live
	if threshold < 4<<20 {
		return 4 << 20
	}
	return threshold
}

// CompactState rewrites only the invalidation journal. Every token and the
// collection epoch remain identical; retained diagnostic events are independent.
func (s *Store) CompactState() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return err
	}
	return s.compactStateLocked()
}
func (s *Store) compactStateLocked() error {
	st := s.state
	snapshot, err := s.snapshotState(Coverage{}, true)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(s.collection, ".collection-state-")
	if err != nil {
		return err
	}
	path := temp.Name()
	defer os.Remove(path)
	if err = temp.Close(); err != nil {
		return err
	}
	output, err := openFile(path, "NUTSSTC6", [32]byte{}, st.epoch, 0)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			output.Close()
		}
	}()
	err = snapshot.visitPages(func(start uint64, page statePage) error {
		encoded, err := encoder.Marshal(invalidation{Snapshot: &page, SnapshotStart: start})
		if err != nil {
			return err
		}
		return appendRecord(output, encoded)
	})
	if err != nil {
		return err
	}
	if err = output.Sync(); err != nil {
		return err
	}
	stat, err := output.Stat()
	if err != nil {
		return err
	}
	if err = os.Rename(path, filepath.Join(s.collection, "collection.state")); err != nil {
		return err
	}
	old := st.file
	st.file = output
	st.journalBytes = stat.Size()
	keep = true
	closeErr := old.Close()
	if err = syncDir(s.collection); err == nil {
		err = closeErr
	}
	if err != nil {
		st.failed = err
	}
	return err
}
func (s *Store) removeStateTemps() error {
	paths, err := filepath.Glob(filepath.Join(s.collection, ".collection-state-*"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err = os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}
