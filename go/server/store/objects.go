package store

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"trie-trunk/go/codec"
)

const minorMask uint32 = (1 << 20) - 1

func (s *schema) directory(root, tap string) string {
	if s.definition.Object == "" {
		return versionDir(root, tap, s.revision.Version)
	}
	return filepath.Join(root, tap, fmt.Sprintf("%s.v%d", s.definition.Object, s.revision.Version>>20), strconv.FormatUint(uint64(s.revision.Version&minorMask), 10))
}

// Reject symbolic links in identity components before publishing files.
func (s *schema) checkDirectory(root, tap string) error {
	dir := s.directory(root, tap)
	paths := []string{dir}
	if s.definition.Object != "" {
		paths = append(paths, filepath.Dir(dir))
	}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("object/schema must be a real directory: %s", path)
		}
	}
	return nil
}

func objectMajor(name string) (string, uint32, bool) {
	i := strings.LastIndex(name, ".v")
	if i < 1 || codec.ValidateObject(name[:i]) != nil {
		return "", 0, false
	}
	n, e := strconv.ParseUint(name[i+2:], 10, 12)
	if e != nil || n == 0 || strconv.FormatUint(n, 10) != name[i+2:] {
		return "", 0, false
	}
	return name[:i], uint32(n), true
}

type schemaDirectory struct {
	path, object string
	version      uint32
}

func schemaDirectories(root, tap string) ([]schemaDirectory, error) {
	base := filepath.Join(root, tap)
	entries, err := os.ReadDir(base)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var result []schemaDirectory
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if object, major, ok := objectMajor(entry.Name()); ok {
			minors, err := os.ReadDir(filepath.Join(base, entry.Name()))
			if err != nil {
				return nil, err
			}
			for _, minor := range minors {
				n, err := strconv.ParseUint(minor.Name(), 10, 20)
				if !minor.IsDir() || err != nil || strconv.FormatUint(n, 10) != minor.Name() {
					continue
				}
				result = append(result, schemaDirectory{filepath.Join(base, entry.Name(), minor.Name()), object, major<<20 | uint32(n)})
			}
		} else if n, err := strconv.ParseUint(entry.Name(), 10, 32); err == nil && n != 0 && strconv.FormatUint(n, 10) == entry.Name() {
			result = append(result, schemaDirectory{filepath.Join(base, entry.Name()), "", uint32(n)})
		}
	}
	return result, nil
}

// The tap registry is a discoverable projection of immutable definitions, rebuilt
// under the tap's writer lock. Definitions remain the source of truth.
type ObjectRegistry struct {
	Format  int                `json:"format"`
	Tap     string             `json:"tap"`
	Objects []RegisteredObject `json:"objects"`
}
type RegisteredObject struct {
	Name        string                 `json:"object_name"`
	Major       uint32                 `json:"major_version"`
	Definitions []RegisteredDefinition `json:"definitions"`
}
type RegisteredDefinition struct {
	Minor  uint32 `json:"minor_version"`
	Digest string `json:"digest"`
	Map    string `json:"map"`
}

func (m *Manager) writeRegistry() error {
	registry := ObjectRegistry{Format: 1, Tap: m.config.Tap, Objects: []RegisteredObject{}}
	groups := map[string]*RegisteredObject{}
	for _, s := range m.schemas {
		if s.definition.Object == "" {
			continue
		}
		major := s.revision.Version >> 20
		key := fmt.Sprintf("%s.v%d", s.definition.Object, major)
		o := groups[key]
		if o == nil {
			o = &RegisteredObject{Name: s.definition.Object, Major: major}
			groups[key] = o
		}
		rel, err := filepath.Rel(filepath.Join(m.config.Root, m.config.Tap), s.directory(m.config.Root, m.config.Tap))
		if err != nil {
			return err
		}
		o.Definitions = append(o.Definitions, RegisteredDefinition{s.revision.Version & minorMask, hex.EncodeToString(s.revision.Digest[:]), filepath.ToSlash(filepath.Join(rel, "events.map"))})
	}
	for _, o := range groups {
		sort.Slice(o.Definitions, func(i, j int) bool { return o.Definitions[i].Minor < o.Definitions[j].Minor })
		registry.Objects = append(registry.Objects, *o)
	}
	sort.Slice(registry.Objects, func(i, j int) bool {
		a, b := registry.Objects[i], registry.Objects[j]
		if a.Name == b.Name {
			return a.Major < b.Major
		}
		return a.Name < b.Name
	})
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	return replaceSnapshot(filepath.Join(m.config.Root, m.config.Tap, "registry.json"), append(data, '\n'))
}

func replaceSnapshot(path string, data []byte) error {
	if existing, err := os.ReadFile(path); err == nil && string(existing) == string(data) {
		return nil
	}
	// installNamedMap provides a synced temporary file, atomic rename, directory sync.
	return installNamedMap(filepath.Dir(path), filepath.Base(path), data)
}
