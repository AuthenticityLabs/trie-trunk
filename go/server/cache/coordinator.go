package cache

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// storeGroup holds a single cache writer lease. Its mutex serializes the shared
// catalog, object postings and invalidation journal across definition minors.
// Each minor retains its own immutable layout, branch files and staging queue.
// Opening a minor under this lease never changes an existing store's definition.
type storeGroup struct {
	mu      sync.Mutex
	lock    *os.File
	stores  map[string]*Store
	objects map[string]*objectIndex
	states  map[string]*objectState
}

func newStoreGroup(c Config) (*storeGroup, error) {
	root := filepath.Join(c.Root, c.Cache)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	lock, err := lockRoot(root)
	if err != nil {
		return nil, err
	}
	return &storeGroup{lock: lock, stores: map[string]*Store{}, objects: map[string]*objectIndex{}, states: map[string]*objectState{}}, nil
}
func (g *storeGroup) open(c Config) (*Store, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := fmt.Sprintf("%x", c.Layout.Digest)
	if s := g.stores[key]; s != nil {
		return s, nil
	}
	c.group = g
	s, err := Open(c)
	if err != nil {
		return nil, err
	}
	g.stores[key] = s
	g.catalog(s.catalog)
	return s, nil
}

// Caller holds the shared store mutex.
func (g *storeGroup) catalog(c Catalog) {
	for _, s := range g.stores {
		s.catalog = c
	}
}
func (g *storeGroup) close() error {
	var err error
	for _, s := range g.stores {
		err = errors.Join(err, s.Close())
	}
	for _, i := range g.objects {
		err = errors.Join(err, i.file.Close())
	}
	for _, s := range g.states {
		err = errors.Join(err, s.file.Close())
	}
	return errors.Join(err, g.lock.Close())
}
