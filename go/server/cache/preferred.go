package cache

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefinitionStatus describes an immutable registered map and the separately
// selected ingestion target. Installing a minor does not promote it.
type DefinitionStatus struct {
	Collection string `json:"collection"`
	Minor      uint32 `json:"minor"`
	Digest     string `json:"digest"`
	Preferred  bool   `json:"preferred"`
}

// Promotion must not strand already registered producers. Their maps are small,
// immutable registration dependencies; this scan occurs only on administration.
func (p *Endpoint) validatePreferred(target *Layout) error {
	key := collectionKey(target)
	checked := map[[32]byte]bool{}
	check := func(source *Layout) error {
		if checked[source.Digest] {
			return nil
		}
		checked[source.Digest] = true
		if _, err := CompileProjection(source, target); err != nil {
			return fmt.Errorf("producer %x: %w", source.Digest, err)
		}
		return nil
	}
	if current := p.preferred[key]; current != nil {
		if err := check(current.Store.layout); err != nil {
			return err
		}
	}
	for _, api := range p.apis {
		source := api.Store.layout
		if collectionKey(source) == key && source.Definition.Version <= target.Definition.Version {
			if err := check(source); err != nil {
				return err
			}
		}
	}
	n := target.Definition.Resource
	pattern := filepath.Join(p.config.Root, p.config.Cache, n.Service, fmt.Sprintf("%s.v%d", n.Resource, n.Major), "producer-*.map")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		source, err := ParseDefinition(raw)
		if err != nil {
			return err
		}
		digest := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "producer-"), ".map")
		if digest != fmt.Sprintf("%x", source.Digest) || collectionKey(source) != key {
			return fmt.Errorf("invalid registered producer binding")
		}
		if err := check(source); err != nil {
			return err
		}
	}
	return nil
}

func (p *Endpoint) Definitions() ([]DefinitionStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, fmt.Errorf("endpoint closed")
	}
	result := make([]DefinitionStatus, 0, len(p.apis))
	for _, api := range p.sources() {
		l := api.Store.layout
		result = append(result, DefinitionStatus{collectionKey(l), l.Definition.Resource.Minor, fmt.Sprintf("%x", l.Digest), p.preferred[collectionKey(l)] == api})
	}
	return result, nil
}

// Caller holds the endpoint mutex, or is still constructing it. Persist only
// after registered immutable maps are durable; historical bindings are untouched.
func (p *Endpoint) persistPreferences() error {
	if p.group == nil || len(p.group.stores) == 0 {
		return nil
	}
	p.group.mu.Lock()
	defer p.group.mu.Unlock()
	var catalog Catalog
	for _, s := range p.group.stores {
		catalog = s.catalog
		break
	}
	if catalog.Definitions == nil {
		return fmt.Errorf("no registered catalog")
	}
	selected := make(map[string][32]byte, len(p.preferred))
	for key, api := range p.preferred {
		selected[key] = api.Store.layout.Digest
	}
	if maps.Equal(catalog.Preferred, selected) {
		return nil
	}
	catalog.Preferred = selected
	raw, err := encoder.Marshal(catalog)
	if err != nil {
		return err
	}
	if err = replace(filepath.Join(p.config.Root, p.config.Cache, "registry.map"), raw); err != nil {
		return err
	}
	p.group.catalog(catalog)
	return nil
}

func (p *Endpoint) selectDefault() {
	if p.api != nil {
		if selected := p.preferred[collectionKey(p.api.Store.layout)]; selected != nil {
			p.api = selected
			return
		}
	}
	keys := make([]string, 0, len(p.preferred))
	for key := range p.preferred {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	p.api = nil
	if len(keys) > 0 {
		p.api = p.preferred[keys[0]]
	}
}
