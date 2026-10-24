package cache

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Capacity closes new ingress across the old minor. Acknowledged seed records
// still drain. Persist the chosen successor before installation so retries and
// crash recovery cannot allocate another minor for the same transition.
func (p *Endpoint) rollover(s *Store) (*Layout, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, fmt.Errorf("endpoint closed")
	}
	key := collectionKey(s.layout)
	target, err := capacityTarget(s.dir)
	if err != nil {
		return nil, err
	}
	if target == 0 {
		minor := s.layout.Definition.Resource.Minor
		for _, api := range p.apis {
			if collectionKey(api.Store.layout) == key && api.Store.layout.Definition.Resource.Minor > minor {
				minor = api.Store.layout.Definition.Resource.Minor
			}
		}
		target = minor + 1
		if target >= 1<<20 {
			return nil, fmt.Errorf("minor address space exhausted; administrator must create a new major")
		}
		s.mu.Lock()
		err = replace(filepath.Join(s.dir, "capacity.closed"), []byte(strconv.FormatUint(uint64(target), 10)+"\n"))
		if err == nil {
			s.capacityClosed = true
		}
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	d := s.layout.Definition
	d.Resource.Minor = target
	d.Version = uint64(d.Resource.Major)<<20 | uint64(target)
	l, err := Compile(d)
	if err != nil {
		return nil, err
	}
	// Per-minor explicit transforms follow an identical field layout; update only
	// their binding digest. Other policies already inherit at resource scope.
	if s.transforms != nil {
		cfg := *s.transforms
		cfg.Target = fmtDigestNative(l.Digest)
		raw, err := toml.Marshal(cfg)
		if err != nil {
			return nil, err
		}
		dir := filepath.Join(s.collection, strconv.FormatUint(uint64(target), 10))
		if err = os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		if err = replace(filepath.Join(dir, "transforms.cfg"), raw); err != nil {
			return nil, err
		}
	}
	if err = p.install(l); err != nil {
		return nil, err
	}
	prior := p.preferred[key]
	if prior != nil && prior.Store.layout.Definition.Version > l.Definition.Version {
		return prior.Store.layout, nil
	}
	p.preferred[key] = p.apis[fmt.Sprintf("%x", l.Digest)]
	if err = p.persistPreferences(); err != nil {
		p.preferred[key] = prior
		return nil, err
	}
	p.selectDefault()
	return l, nil
}
func capacityTarget(dir string) (uint32, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "capacity.closed"))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 20)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("invalid capacity successor")
	}
	return uint32(n), nil
}
func (s *Store) loadCapacity() error {
	target, err := capacityTarget(s.dir)
	if err == nil && target != 0 && target <= s.layout.Definition.Resource.Minor {
		return fmt.Errorf("capacity successor must advance minor")
	}
	s.capacityClosed = target != 0
	return err
}
