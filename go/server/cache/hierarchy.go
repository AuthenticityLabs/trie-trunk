package cache

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	return resolveScope(raw, path, parent, branch)
}

func resolveScope(raw []byte, path string, parent Policy, branch bool) (Policy, bool, error) {
	var err error
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
	n := s.layout.Definition.Resource
	p, err := configuredScope(s.config.Root, []string{s.config.Cache, n.Service, key[strings.LastIndex(key, "/")+1:]}, c)
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

// Shared by the runtime and configuration editor so displayed inheritance follows
// precisely the same registry and filesystem precedence as ingestion.
func configuredScope(root string, scope []string, c Configuration) (Policy, error) {
	p, _, err := readScope(filepath.Join(filepath.Dir(root), "trunk.cfg"), Policy{}, false)
	if err != nil || len(scope) == 0 {
		return p, err
	}
	p = mergePolicy(p, c.Defaults)
	for depth := 1; depth <= len(scope); depth++ {
		if depth == 3 {
			p = mergePolicy(p, c.Resources[scope[1]+"/"+scope[2]])
		}
		p, _, err = readScope(configurationPath(root, scope[:depth]), p, depth == 4)
		if err != nil {
			return p, err
		}
	}
	return p, nil
}

// Callers must verify scope against the physical catalog before resolving paths.
func configurationPath(root string, scope []string) string {
	switch len(scope) {
	case 0:
		return filepath.Join(filepath.Dir(root), "trunk.cfg")
	case 1:
		return filepath.Join(root, scope[0], "cache.cfg")
	case 2:
		return filepath.Join(root, scope[0], scope[1], "service.cfg")
	case 3:
		return filepath.Join(root, scope[0], scope[1], scope[2], "resource.cfg")
	default:
		return filepath.Join(root, scope[0], scope[1], scope[2], scope[3]+".cfg")
	}
}
