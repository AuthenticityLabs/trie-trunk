package cache

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// All scope files are optional. An omitted inherit means true. False starts at
// built-in defaults for this scope; explicit false and zero policy values remain
// real overrides. Immutable definition identity/timezone do not live here.
type scopeConfiguration struct {
	Version uint32 `toml:"version"`
	Inherit *bool  `toml:"inherit"`
	Policy  Policy `toml:"policy"`
}

func readScope(path string, parent Policy, branch bool) (Policy, bool, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return parent, false, nil
	}
	if err != nil {
		return parent, false, err
	}
	if len(raw) > 1<<20 {
		return parent, false, fmt.Errorf("%s: configuration too large", path)
	}
	var c scopeConfiguration
	if err = toml.NewDecoder(bytes.NewReader(raw)).DisallowUnknownFields().Decode(&c); err != nil {
		return parent, false, fmt.Errorf("%s: %w", path, err)
	}
	if c.Version != 1 {
		return parent, false, fmt.Errorf("%s: unsupported configuration version", path)
	}
	if err = c.Policy.validate(); err != nil {
		return parent, false, fmt.Errorf("%s: %w", path, err)
	}
	if branch && (c.Policy.ObjectIndex != nil || c.Policy.Staging != nil) {
		return parent, false, fmt.Errorf("%s: object_index and staging capacity belong at resource scope or above", path)
	}
	if c.Inherit != nil && !*c.Inherit {
		parent = Policy{}
	}
	p := mergePolicy(parent, c.Policy)
	// Staging fields inherit individually across filesystem scopes. Preserve
	// explicit zero (unlimited), unlike an absent TOML key.
	if c.Policy.Staging != nil && parent.Staging != nil {
		var present struct {
			Policy struct {
				Staging map[string]int64 `toml:"staging"`
			} `toml:"policy"`
		}
		if err = toml.Unmarshal(raw, &present); err != nil {
			return parent, false, err
		}
		next := *parent.Staging
		for key, value := range present.Policy.Staging {
			switch key {
			case "max_bytes":
				next.MaxBytes = value
			case "max_records":
				next.MaxRecords = value
			case "max_disk_bytes":
				next.MaxDiskBytes = value
			}
		}
		p.Staging = &next
	}
	return p, true, nil
}

func (s *Store) hierarchicalPolicy(c Configuration, key string) (Policy, map[Branch]Policy, error) {
	p, _, err := readScope(filepath.Join(filepath.Dir(s.config.Root), "trunk.cfg"), Policy{}, false)
	if err != nil {
		return Policy{}, nil, err
	}
	p = mergePolicy(p, c.Defaults)
	// registry.cfg remains accepted; an optional cache.cfg has nearer precedence.
	for _, path := range []string{filepath.Join(s.root, "cache.cfg"), filepath.Join(s.root, s.layout.Definition.Resource.Service, "service.cfg")} {
		p, _, err = readScope(path, p, false)
		if err != nil {
			return Policy{}, nil, err
		}
	}
	p = mergePolicy(p, c.Resources[key])
	p, _, err = readScope(filepath.Join(s.collection, "resource.cfg"), p, false)
	if err != nil {
		return Policy{}, nil, err
	}
	branches := map[Branch]Policy{}
	for branch := Read; branch <= Trace; branch++ {
		q, exists, e := readScope(filepath.Join(s.collection, branch.Prefix()+".cfg"), p, true)
		if e != nil {
			return Policy{}, nil, e
		}
		if exists {
			branches[branch] = q
		}
	}
	return p, branches, nil
}

func (s *Store) policyFor(branch Branch) Policy {
	if p, ok := s.branchPolicies[branch]; ok {
		return p
	}
	return s.policy
}
