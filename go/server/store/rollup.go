package store

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"trie-trunk/go/codec"
)

func (m *Manager) PublishRollup(rev codec.Revision, resolution Resolution, at time.Time, groups map[uint32]Aggregate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(); err != nil {
		return err
	}
	s, ok := m.schemas[rev.Digest]
	if !ok || s.revision != rev {
		return ErrConflict
	}
	path, bucket, end, err := schemaRollupPath(s.directory(m.config.Root, m.config.Tap), resolution, at, s.location, strings.TrimSuffix(s.filename(""), "."))
	if err != nil {
		return err
	}
	if m.config.Now().Before(end) {
		return fmt.Errorf("aggregate period is still open")
	}
	if len(groups) > m.config.MaxGroups {
		return ErrLimit
	}
	tr, err := acquireTrie(m.config, s)
	if err != nil {
		return m.fail(err)
	}
	defer tr.release()
	leaves := make([]uint32, 0, len(groups))
	for leaf, a := range groups {
		if uint64(leaf) >= uint64(len(tr.nodes)) || a.Count == 0 || a.Min > a.Max || a.Sum < a.Max || a.Min > a.Sum/a.Count {
			return ErrInvalid
		}
		leaves = append(leaves, leaf)
	}
	sort.Slice(leaves, func(i, j int) bool { return leaves[i] < leaves[j] })
	data := make([]byte, 6+36*len(leaves))
	order.PutUint16(data[:2], bucket)
	order.PutUint32(data[2:6], uint32(len(leaves)))
	for i, leaf := range leaves {
		a := groups[leaf]
		pos := 6 + 36*i
		order.PutUint32(data[pos:pos+4], leaf)
		order.PutUint64(data[pos+4:pos+12], a.Count)
		order.PutUint64(data[pos+12:pos+20], a.Sum)
		order.PutUint64(data[pos+20:pos+28], a.Min)
		order.PutUint64(data[pos+28:pos+36], a.Max)
	}
	if err = tr.file.Sync(); err != nil {
		return m.fail(err)
	}
	if err = mkdirDurable(filepath.Dir(path)); err != nil {
		return m.fail(err)
	}
	f, err := s.openLog(path, "TAGG")
	if err != nil {
		return m.fail(err)
	}
	defer f.Close()
	found := false
	err = scan(f, func(_, _ int64, b []byte) error {
		key, _, e := decodeRollup(b)
		if e != nil {
			return e
		}
		if key == bucket {
			if found || !bytes.Equal(b, data) {
				return ErrConflict
			}
			found = true
		}
		return nil
	})
	if err != nil {
		if err == ErrConflict {
			return err
		}
		return m.fail(err)
	}
	if !found {
		if _, err = appendBlock(f, data); err != nil {
			return m.fail(err)
		}
	}
	if err = f.Sync(); err != nil {
		return m.fail(err)
	}
	return m.fail(syncDirectory(filepath.Dir(path)))
}

func (m *Manager) ReadRollup(rev codec.Revision, resolution Resolution, at time.Time) (map[uint32]Aggregate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(); err != nil {
		return nil, err
	}
	s, ok := m.schemas[rev.Digest]
	if !ok || s.revision != rev {
		return nil, ErrConflict
	}
	path, bucket, _, err := schemaRollupPath(s.directory(m.config.Root, m.config.Tap), resolution, at, s.location, strings.TrimSuffix(s.filename(""), "."))
	if err != nil {
		return nil, err
	}
	if _, err = os.Stat(path); err != nil {
		return nil, err
	}
	f, err := s.openLog(path, "TAGG")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var result map[uint32]Aggregate
	err = scan(f, func(_, _ int64, b []byte) error {
		key, groups, e := decodeRollup(b)
		if e != nil {
			return e
		}
		if key == bucket {
			if result != nil {
				return fmt.Errorf("duplicate aggregate period")
			}
			result = groups
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, os.ErrNotExist
	}
	return result, nil
}

func decodeRollup(b []byte) (uint16, map[uint32]Aggregate, error) {
	if len(b) < 6 {
		return 0, nil, fmt.Errorf("short rollup")
	}
	key := order.Uint16(b[:2])
	n := int(order.Uint32(b[2:6]))
	if key > 366 || n > (maxBlockSize-6)/36 || len(b) != 6+n*36 {
		return 0, nil, fmt.Errorf("invalid rollup length")
	}
	groups := make(map[uint32]Aggregate, n)
	var previous uint32
	for i := 0; i < n; i++ {
		pos := 6 + 36*i
		leaf := order.Uint32(b[pos : pos+4])
		a := Aggregate{order.Uint64(b[pos+4 : pos+12]), order.Uint64(b[pos+12 : pos+20]), order.Uint64(b[pos+20 : pos+28]), order.Uint64(b[pos+28 : pos+36])}
		if i > 0 && leaf <= previous || a.Count == 0 || a.Min > a.Max || a.Sum < a.Max || a.Min > a.Sum/a.Count {
			return 0, nil, fmt.Errorf("invalid rollup group")
		}
		groups[leaf] = a
		previous = leaf
	}
	return key, groups, nil
}
