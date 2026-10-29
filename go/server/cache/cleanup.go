package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Cleanup is offline maintenance: it takes the same exclusive lease as writers,
// but does not open/recover any store or alter the catalog. Preview is the default.
// Registered maps and producer maps are durable identities, not expendable caches.
type CleanupFile struct {
	Path    string `json:"path"`
	Bytes   int64  `json:"bytes"`
	Reason  string `json:"reason"`
	Removed bool   `json:"removed"`
	digest  [32]byte
}
type CleanupReport struct {
	Cache string        `json:"cache"`
	Files []CleanupFile `json:"files"`
	Bytes int64         `json:"bytes"`
}

var generationFile = regexp.MustCompile(`^(read|write|error|debug|trace)(?:\.([gr][0-9a-f]+))?\.(idx|pods|tri|journal|sum|365|[wmy][0-9]{2})$`)

func Cleanup(ctx context.Context, c Config, apply bool) (report CleanupReport, err error) {
	if !name(c.Cache) || c.Root == "" || c.Tenant == "" {
		return report, fmt.Errorf("cache, tenant and root required")
	}
	root := filepath.Join(c.Root, c.Cache)
	info, err := os.Lstat(root)
	if err != nil {
		return report, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return report, fmt.Errorf("cache root must be a directory")
	}
	lock, err := lockRoot(root)
	if err != nil {
		return report, err
	}
	defer lock.Close()
	raw, err := os.ReadFile(filepath.Join(root, "registry.map"))
	if err != nil {
		return report, err
	}
	var catalog Catalog
	if err = decoder.Unmarshal(raw, &catalog); err != nil {
		return report, err
	}
	if catalog.Format != Format || catalog.Cache != c.Cache || catalog.Tenant != c.Tenant || catalog.Definitions == nil {
		return report, fmt.Errorf("catalog ownership mismatch")
	}
	report.Cache = c.Cache
	layouts := map[string]*Layout{}
	reachable := map[string]bool{}
	for binding, digest := range catalog.Definitions {
		if splitBinding(binding) == nil {
			return report, fmt.Errorf("invalid catalog binding")
		}
		data, e := os.ReadFile(filepath.Join(root, binding, "resource.rx"))
		if e != nil {
			return report, e
		}
		l, e := ParseDefinition(data)
		if e != nil {
			return report, e
		}
		n := l.Definition.Resource
		if fmt.Sprintf("%s/%s.v%d/%d", n.Service, n.Resource, n.Major, n.Minor) != binding || l.Digest != digest {
			return report, fmt.Errorf("catalog definition mismatch")
		}
		layouts[binding] = l
		reachable[fmt.Sprintf("%x", digest)] = true
	}
	for key, digest := range catalog.Preferred {
		found := false
		for _, l := range layouts {
			if collectionKey(l) == key && l.Digest == digest {
				found = true
				break
			}
		}
		if !found {
			return report, fmt.Errorf("invalid preferred definition: %s", key)
		}
	}
	for binding, branches := range catalog.Generations {
		if layouts[binding] == nil {
			return report, fmt.Errorf("generation for unknown definition")
		}
		for branch := range branches {
			if !branch.Valid() {
				return report, fmt.Errorf("invalid generation branch")
			}
		}
	}
	// Inspect the entire tree before unlinking anything, including symlinks. Maps
	// referenced by explicit REF16 translations remain reachable even if not installed.
	verified := map[string]bool{}
	var paths []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("cleanup refuses symlink: %s", path)
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("cleanup refuses non-regular file: %s", path)
		}
		paths = append(paths, path)
		if d.Name() == "resource.rx" || strings.HasPrefix(d.Name(), "producer-") && strings.HasSuffix(d.Name(), ".rx") {
			data, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			l, e := ParseDefinition(data)
			if e != nil {
				return e
			}
			if strings.HasPrefix(d.Name(), "producer-") {
				reachable[fmt.Sprintf("%x", l.Digest)] = true
			}
			for _, r := range l.Definition.Resource.References {
				reachable[r.Source] = true
			}
		}
		return nil
	})
	if err != nil {
		return report, err
	}
	add := func(path, reason string) error {
		size, hash, e := cleanupHash(path)
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		report.Files = append(report.Files, CleanupFile{Path: rel, Bytes: size, Reason: reason, digest: hash})
		report.Bytes += size
		return nil
	}
	for _, path := range paths {
		if err = ctx.Err(); err != nil {
			return report, err
		}
		rel, _ := filepath.Rel(root, path)
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) < 4 {
			continue
		}
		binding := strings.Join(parts[:3], "/")
		l := layouts[binding]
		if l == nil {
			// A crash between ensureMap and catalog publication can leave just this
			// definition. Never infer that an unregistered data directory is disposable.
			if len(parts) != 4 || parts[3] != "resource.rx" || splitBinding(binding) == nil {
				continue
			}
			data, e := os.ReadFile(path)
			if e != nil {
				return report, e
			}
			orphan, e := ParseDefinition(data)
			if e != nil {
				return report, e
			}
			n := orphan.Definition.Resource
			if fmt.Sprintf("%s/%s.v%d/%d", n.Service, n.Resource, n.Major, n.Minor) != binding || reachable[fmt.Sprintf("%x", orphan.Digest)] {
				continue
			}
			siblings, e := os.ReadDir(filepath.Dir(path))
			if e != nil {
				return report, e
			}
			if len(siblings) != 1 {
				continue
			}
			if err = add(path, "unpublished definition without data or references"); err != nil {
				return report, err
			}
			continue
		}
		match := generationFile.FindStringSubmatch(filepath.Base(path))
		if match == nil {
			continue
		}
		// Data files live at the minor root; rollups may live in its year directory.
		if len(parts) > 5 || len(parts) == 5 && (len(parts[3]) != 4 || strings.Trim(parts[3], "0123456789") != "") {
			continue
		}
		var branch Branch
		for b := Read; b <= Trace; b++ {
			if b.Prefix() == match[1] {
				branch = b
				break
			}
		}
		selected, hasSelection := catalog.Generations[binding][branch]
		active := branch.Prefix()
		if hasSelection {
			active += ".g" + hex.EncodeToString(selected[:])
		}
		if n := catalog.RetiredRows[binding][branch]; n > 0 {
			active = fmt.Sprintf("%s.r%d", branch.Prefix(), n)
		}
		if match[3] == "365" || strings.HasPrefix(match[3], "w") || strings.HasPrefix(match[3], "m") || strings.HasPrefix(match[3], "y") {
			active = branch.Prefix()
		}
		prefix := strings.TrimSuffix(filepath.Base(path), "."+match[3])
		if prefix == active {
			continue
		}
		validationKey := binding + "/" + active
		if !verified[validationKey] {
			base := filepath.Join(root, binding, active)
			if catalog.RetiredRows[binding][branch] > 0 {
				if _, e := os.Stat(base + ".journal"); e != nil {
					return report, fmt.Errorf("missing retained branch: %w", e)
				}
			}
			if e := verifyCleanupGeneration(ctx, base, l, selected, hasSelection); e != nil {
				return report, e
			}
			verified[validationKey] = true
		}
		if match[3] == "tri" {
			raw, e := os.ReadFile(path)
			if e != nil {
				return report, e
			}
			if len(raw) < TimeTrieHeader {
				return report, fmt.Errorf("short orphan time trie")
			}
			var source [16]byte
			copy(source[:], raw[48:64])
			if _, e = parseTimeDirectory(raw, source); e != nil {
				return report, e
			}
			if strings.HasPrefix(match[2], "g") && hex.EncodeToString(source[:]) != match[2][1:] {
				return report, fmt.Errorf("orphan time trie source mismatch")
			}
			if err = add(path, "generation not selected by registry.map"); err != nil {
				return report, err
			}
			continue
		}
		// Both old default generations and failed/new unpublished generations are
		// disposable, but only when their header agrees with the definition/name.
		f, e := os.Open(path)
		if e != nil {
			return report, e
		}
		var h [headerSize]byte
		_, e = io.ReadFull(f, h[:])
		f.Close()
		if e != nil {
			return report, fmt.Errorf("unverified orphan %s: %w", rel, e)
		}
		magic := "NUTSAGG6"
		width := uint32(0)
		switch match[3] {
		case "idx":
			magic = "NUTSIDX6"
			width = uint32(l.RowLayout().Width)
		case "pods":
			magic = "PILPOD00"
		case "journal":
			magic = "TTCOMM00"
			width = uint32(l.RowLayout().Width)
		case "sum":
			magic = "NUTSSUM6"
		}
		if string(h[:8]) != magic || !bytes.Equal(h[8:40], l.Digest[:]) || order.Uint32(h[56:60]) != width || order.Uint32(h[60:]) != Format {
			return report, fmt.Errorf("unverified orphan header: %s", rel)
		}
		if strings.HasPrefix(match[2], "g") && hex.EncodeToString(h[40:56]) != match[2][1:] {
			return report, fmt.Errorf("orphan generation mismatch: %s", rel)
		}
		if err = add(path, "generation not selected by registry.map"); err != nil {
			return report, err
		}
	}
	sort.Slice(report.Files, func(i, j int) bool { return report.Files[i].Path < report.Files[j].Path })
	if !apply {
		return report, nil
	}
	// Validate the complete candidate list before deletion. The writer lease is
	// held throughout; another managed process cannot replace its dependencies.
	for _, item := range report.Files {
		size, hash, e := cleanupHash(filepath.Join(root, item.Path))
		if e != nil {
			return report, e
		}
		if size != item.Bytes || hash != item.digest {
			return report, fmt.Errorf("cleanup candidate changed: %s", item.Path)
		}
	}
	for i := range report.Files {
		if err = ctx.Err(); err != nil {
			return report, err
		}
		path := filepath.Join(root, report.Files[i].Path)
		if err = os.Remove(path); err != nil {
			return report, err
		}
		report.Files[i].Removed = true
		if err = syncDir(filepath.Dir(path)); err != nil {
			return report, err
		}
	}
	return report, nil
}
func cleanupHash(path string) (int64, [32]byte, error) {
	var result [32]byte
	f, e := os.Open(path)
	if e != nil {
		return 0, result, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	copy(result[:], h.Sum(nil))
	return n, result, e
}

// A corrupt/missing selected replacement must never make its predecessor garbage.
// Validate committed index/payload checksums without recovering or truncating
// source files. Incomplete active journals require normal recovery before cleanup.
func verifyCleanupGeneration(ctx context.Context, base string, l *Layout, generation [16]byte, explicit bool) error {
	tri, e := os.Open(base + ".journal")
	if os.IsNotExist(e) && !explicit {
		for _, ext := range []string{".idx", ".pods", ".sum"} {
			if _, err := os.Stat(base + ext); !os.IsNotExist(err) {
				return fmt.Errorf("incomplete active generation: %s", base)
			}
		}
		return nil
	}
	if e != nil {
		return e
	}
	defer tri.Close()
	idx, e := os.Open(base + ".idx")
	if e != nil {
		return e
	}
	defer idx.Close()
	pods, e := os.Open(base + ".pods")
	if e != nil {
		return e
	}
	defer pods.Close()
	var actualGeneration []byte
	for _, item := range []struct {
		f     *os.File
		magic string
		width uint32
	}{{tri, "TTCOMM00", IndexRowSize}, {idx, "NUTSIDX6", IndexRowSize}, {pods, "PILPOD00", 0}} {
		var h [headerSize]byte
		if _, e = io.ReadFull(item.f, h[:]); e != nil {
			return e
		}
		if string(h[:8]) != item.magic || !bytes.Equal(h[8:40], l.Digest[:]) || order.Uint32(h[56:60]) != item.width || order.Uint32(h[60:]) != Format {
			return fmt.Errorf("invalid active generation header")
		}
		if explicit && !bytes.Equal(h[40:56], generation[:]) {
			return fmt.Errorf("active generation mismatch")
		}
		if actualGeneration == nil {
			actualGeneration = append([]byte(nil), h[40:56]...)
		} else if !bytes.Equal(actualGeneration, h[40:56]) {
			return fmt.Errorf("active file generation mismatch")
		}
	}
	podsRetired, e := retiredBytes(pods)
	if e != nil {
		return e
	}
	baseRows := uint64(0)
	tail, podsEnd := uint64(0), uint64(headerSize)+podsRetired
	for {
		if e = ctx.Err(); e != nil {
			return e
		}
		var header [8]byte
		n, err := io.ReadFull(tri, header[:])
		if err == io.EOF && n == 0 {
			return nil
		}
		if err != nil {
			return fmt.Errorf("active journal requires recovery: %w", err)
		}
		size := order.Uint32(header[:4])
		if size == 0 || size > MaxFrame {
			return fmt.Errorf("invalid active commit size")
		}
		raw := make([]byte, size)
		if _, e = io.ReadFull(tri, raw); e != nil {
			return e
		}
		if crc32.Checksum(raw, checksum) != order.Uint32(header[4:]) {
			return fmt.Errorf("active commit checksum mismatch")
		}
		var commit Commit
		if e = decoder.Unmarshal(raw, &commit); e != nil {
			return e
		}
		if commit.Retired != nil {
			baseRows = commit.Retired.Rows
			tail = baseRows
			continue
		}
		r := commit.Run
		if r.Start != tail+IndexHeaderSlots || r.End <= r.Start || r.End > IndexCapacity || r.End-r.Start > uint64(PACMaximumMembers) || r.End > (1<<63-1-headerSize)/IndexRowSize || r.PodsStart != podsEnd || r.PodsEnd < r.PodsStart || r.PodsEnd > 1<<63-1 {
			return fmt.Errorf("invalid active commit bounds")
		}
		for _, part := range []struct {
			f        *os.File
			from, to uint64
			crc      uint32
		}{{idx, headerSize + IndexRowSize*(r.Start-baseRows), headerSize + IndexRowSize*(r.End-baseRows), r.IndexCRC}, {pods, r.PodsStart - podsRetired, r.PodsEnd - podsRetired, r.PodsCRC}} {
			h := crc32.New(checksum)
			n, e := io.Copy(h, io.NewSectionReader(part.f, int64(part.from), int64(part.to-part.from)))
			if e != nil {
				return e
			}
			if uint64(n) != part.to-part.from || h.Sum32() != part.crc {
				return fmt.Errorf("active committed data checksum mismatch")
			}
		}
		tail, podsEnd = r.End, r.PodsEnd
	}
}
