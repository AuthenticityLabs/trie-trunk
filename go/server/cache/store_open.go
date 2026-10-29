package cache

// Open, validate and recover canonical files before publishing the store.
import (
	"crypto/rand"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Open takes one writer lease for this cache and recovers committed storage.
// Definitions are immutable; format experiments regenerate disposable test data.
func Open(c Config) (s *Store, err error) {
	if !name(c.Cache) || c.Tenant == "" || c.Root == "" || c.Layout == nil {
		return nil, fmt.Errorf("cache, tenant, root and definition required")
	}
	l, e := ParseDefinition(c.Layout.Bytes)
	if e != nil {
		return nil, e
	}
	if c.ObjectIndex && l.Object < 0 {
		return nil, fmt.Errorf("object index requires an object identity role")
	}
	if c.BatchRecords <= 0 {
		c.BatchRecords = 1024
	}
	if c.BatchRecords > 65536 {
		return nil, fmt.Errorf("batch record limit exceeds journal capacity")
	}
	if c.BatchBytes <= 0 {
		c.BatchBytes = 4 << 20
	}
	if c.BatchBytes > MaxFrame*4 {
		return nil, fmt.Errorf("batch too large")
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.LiveIndexRecords <= 0 {
		c.LiveIndexRecords = 1_000_000
	}
	if c.NativeMemoryBytes == 0 {
		c.NativeMemoryBytes = 1 << 30
	}
	if c.Sealer == nil {
		c.Sealer = defaultSealScheduler
	}
	s = &Store{mu: &sync.Mutex{}, config: c, requestedObjectIndex: c.ObjectIndex, layout: l, row: l.RowLayout(), trie: newTrie(), branches: map[Branch]*branchStore{}}
	s.location = time.UTC
	s.root = filepath.Join(c.Root, c.Cache)
	n := l.Definition.Resource
	s.collection = filepath.Join(s.root, n.Service, fmt.Sprintf("%s.v%d", n.Resource, n.Major))
	s.dir = filepath.Join(s.collection, fmt.Sprint(n.Minor))
	if err = os.MkdirAll(s.dir, 0700); err != nil {
		return nil, err
	}
	if c.group != nil {
		s.mu = &c.group.mu
	} else {
		s.lock, err = lockRoot(s.root)
		if err != nil {
			return nil, err
		}
	}
	cleanup := s
	defer func() {
		if err != nil {
			cleanup.release()
		}
	}()
	// Version-zero fixtures are recreated when their storage names change.
	// Never open an empty signature trie beside an old one: index references
	// would silently lose their meaning.
	if _, e := os.Stat(filepath.Join(s.dir, "resource.tri")); e == nil {
		return nil, fmt.Errorf("obsolete version-zero resource.tri in %s; recreate the cache using state.tri", s.dir)
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if err = s.removeSwapTemps(); err != nil {
		return nil, err
	}
	s.memory = newNativeMemory(c.NativeMemoryBytes)
	path := filepath.Join(s.root, "registry.map")
	catalog := Catalog{Format: Format, Cache: c.Cache, Tenant: c.Tenant, Definitions: map[string][32]byte{}}
	if raw, e := os.ReadFile(path); e == nil {
		if e = decoder.Unmarshal(raw, &catalog); e != nil {
			return nil, e
		}
		if catalog.Format != Format || catalog.Cache != c.Cache || catalog.Tenant != c.Tenant {
			return nil, fmt.Errorf("catalog ownership mismatch")
		}
		if catalog.Definitions == nil {
			return nil, fmt.Errorf("invalid catalog")
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if err = s.loadPolicy(); err != nil {
		return nil, err
	}
	if err = s.loadCapacity(); err != nil {
		return nil, err
	}
	if err = s.loadTransforms(); err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%s/%s.v%d/%d", n.Service, n.Resource, n.Major, n.Minor)
	s.binding = key
	if digest, ok := catalog.Definitions[key]; ok && digest != l.Digest {
		return nil, fmt.Errorf("immutable definition conflict")
	}
	for binding := range catalog.Definitions {
		p := splitBinding(binding)
		if p == nil {
			return nil, fmt.Errorf("invalid catalog binding")
		}
		if p.domain != n.Service || p.collection != n.Resource || p.major != n.Major || p.minor == n.Minor {
			continue
		}
		raw, e := os.ReadFile(filepath.Join(s.root, binding, "resource.rx"))
		if e != nil {
			return nil, e
		}
		prior, e := ParseDefinition(raw)
		if e != nil {
			return nil, e
		}
		for i, field := range []int{l.Object, l.Revision} {
			old := prior.Object
			if i == 1 {
				old = prior.Revision
			}
			if (field < 0) != (old < 0) || field >= 0 && l.Definition.Fields[field].Type != prior.Definition.Fields[old].Type {
				return nil, fmt.Errorf("identity type change requires a new object major")
			}
		}
	}
	if err = ensureMap(filepath.Join(s.dir, "resource.rx"), l.Bytes); err != nil {
		return nil, err
	}
	catalog.Definitions[key] = l.Digest
	if catalog.Generations == nil {
		catalog.Generations = map[string]map[Branch][16]byte{}
	}
	s.catalog = catalog
	b, e := encoder.Marshal(catalog)
	if e != nil {
		return nil, e
	}
	if err = replace(path, b); err != nil {
		return nil, err
	}
	// The taxonomy is small and compiled once, separate from event classifications.
	taxonomy := map[string]map[string][]uint32{}
	for key := range catalog.Definitions {
		var domain, collection string
		var major, minor uint32
		// Parse registered paths rather than infer names from filesystem enumeration.
		parts := splitBinding(key)
		if parts == nil {
			return nil, fmt.Errorf("invalid catalog binding")
		}
		domain = parts.domain
		collection = parts.collection
		major = parts.major
		minor = parts.minor
		if taxonomy[domain] == nil {
			taxonomy[domain] = map[string][]uint32{}
		}
		k := fmt.Sprintf("%s.v%d", collection, major)
		taxonomy[domain][k] = append(taxonomy[domain][k], minor)
	}
	for _, collections := range taxonomy {
		for k, minors := range collections {
			sort.Slice(minors, func(i, j int) bool { return minors[i] < minors[j] })
			collections[k] = minors
		}
	}
	b, e = encoder.Marshal(taxonomy)
	if e != nil {
		return nil, e
	}
	if err = replace(filepath.Join(s.root, "resource.tri"), b); err != nil {
		return nil, err
	}
	s.shared, err = openFile(filepath.Join(s.dir, "state.tri"), "NUTSTRI6", l.Digest, [16]byte{}, 0)
	if err != nil {
		return nil, err
	}
	err = records(s.shared, func(raw []byte) error {
		var nodes []sharedNode
		if e := decoder.Unmarshal(raw, &nodes); e != nil {
			return e
		}
		for _, node := range nodes {
			if e := s.trie.add(node, l); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.trie.persisted = len(s.trie.nodes)
	if err = s.openObjects(); err != nil {
		return nil, err
	}
	if err = s.openState(); err != nil {
		return nil, err
	}
	for branch := Read; branch <= Trace; branch++ {
		if _, e = os.Stat(filepath.Join(s.dir, s.prefix(branch)+".journal")); e == nil {
			if _, err = s.openBranch(branch); err != nil {
				return nil, err
			}
		} else if !os.IsNotExist(e) {
			return nil, e
		} else if _, required := s.catalog.Generations[s.binding][branch]; required || s.catalog.RetiredRows[s.binding][branch] > 0 {
			return nil, fmt.Errorf("catalog-selected branch is missing")
		} else {
			// Version-zero data is regenerated, never silently ignored or migrated.
			for _, ext := range []string{".idx", ".pods", ".tri"} {
				if _, err := os.Stat(filepath.Join(s.dir, s.prefix(branch)+ext)); err == nil {
					return nil, fmt.Errorf("branch %s has data without its commit journal; regenerate version-zero fixtures", branch.Name())
				} else if !os.IsNotExist(err) {
					return nil, err
				}
			}
		}
	}
	if b := s.branches[Write]; b != nil {
		if err = s.indexObjects(b); err != nil {
			return nil, err
		}
	}
	return s, nil
}
func (s *Store) openBranch(branch Branch) (b *branchStore, err error) {
	if !branch.Valid() {
		return nil, fmt.Errorf("invalid branch")
	}
	if b = s.branches[branch]; b != nil {
		return b, nil
	}
	b = &branchStore{branch: branch, layout: s.layout, podsEnd: headerSize, sumEnd: headerSize}
	base := filepath.Join(s.dir, s.prefix(branch))
	raw, e := os.ReadFile(base + ".journal")
	if e == nil {
		if len(raw) < headerSize {
			return nil, fmt.Errorf("short branch header")
		}
		copy(b.generation[:], raw[40:56])
	} else if os.IsNotExist(e) {
		if _, e = rand.Read(b.generation[:]); e != nil {
			return nil, e
		}
	} else {
		return nil, e
	}
	cleanup := b
	defer func() {
		if err != nil {
			cleanup.close()
		}
	}()
	b.live = newNativeLive(s.config.LiveIndexRecords)
	if b.journal, err = openFile(base+".journal", "TTCOMM00", s.layout.Digest, b.generation, s.row.Width); err != nil {
		return nil, err
	}
	if b.idx, err = openFile(base+".idx", "NUTSIDX6", s.layout.Digest, b.generation, s.row.Width); err != nil {
		return nil, err
	}
	if b.pods, err = openFile(base+".pods", "PILPOD00", s.layout.Digest, b.generation, 0); err != nil {
		return nil, err
	}
	if branch.Aggregates() && len(s.layout.Definition.Resource.Aggregations) > 0 {
		if b.sum, err = openFile(base+".sum", "NUTSSUM6", s.layout.Digest, b.generation, 0); err != nil {
			return nil, err
		}
	}
	b.podsRetired, err = retiredBytes(b.pods)
	if err != nil {
		return nil, err
	}
	b.podsEnd += b.podsRetired
	var lastMS int64 = -1
	err = records(b.journal, func(raw []byte) error {
		var commit Commit
		if e := decoder.Unmarshal(raw, &commit); e != nil {
			return e
		}
		if commit.Retired != nil {
			if len(b.runs) > 0 || b.baseRows != 0 {
				return fmt.Errorf("misplaced retirement record")
			}
			r := commit.Retired
			b.baseRows = r.Rows
			b.blockBase = r.Block
			b.memberEnd = r.Members
			b.finalizedBefore = r.Cutoff
			b.timeAnchor = r.Time
			return nil
		}
		run := commit.Run
		tail := b.baseRows
		if len(b.runs) > 0 {
			tail = b.runs[len(b.runs)-1].End
		}
		if run.Start != tail+IndexHeaderSlots || run.End <= run.Start || run.End > IndexCapacity || run.End-run.Start > uint64(PACMaximumMembers) || run.PodsStart != b.podsEnd || run.PodsEnd < run.PodsStart || run.TrieSize > len(s.trie.nodes) || run.TrieSize < 1 {
			return fmt.Errorf("invalid committed run")
		}
		idx, e := readBytes(b.idx, headerSize+(run.Start-b.baseRows)*uint64(s.row.Width), headerSize+(run.End-b.baseRows)*uint64(s.row.Width))
		if e != nil {
			return e
		}
		pods, e := readBytes(b.pods, run.PodsStart-b.podsRetired, run.PodsEnd-b.podsRetired)
		if e != nil {
			return e
		}
		if crc32.Checksum(idx, checksum) != run.IndexCRC || crc32.Checksum(pods, checksum) != run.PodsCRC {
			return fmt.Errorf("committed data checksum mismatch")
		}
		memberStart := b.memberEnd
		if len(b.runs) > 0 {
			prior := b.runs[len(b.runs)-1].PAC.Placement
			memberStart = prior.MemberStart + prior.MemberBytes
		}
		if run.PAC == nil || run.PAC.Placement.MemberStart != memberStart+4+4*(run.End-run.Start) || run.PAC.Placement.Block != b.blockBase+uint32(len(b.runs)) {
			return fmt.Errorf("noncontiguous PAC block directory")
		}
		head, e := readBytes(b.idx, headerSize+(run.Start-IndexHeaderSlots-b.baseRows)*IndexRowSize, headerSize+(run.Start-b.baseRows)*IndexRowSize)
		if e != nil {
			return e
		}
		if e = validateIndexHeader(head, run); e != nil {
			return e
		}
		firstMS := indexTime(idx, run.PAC.Placement.From)
		if lastMS >= firstMS {
			return fmt.Errorf("unordered committed hours")
		}
		lastMS = indexTime(idx[len(idx)-s.row.Width:], run.PAC.Placement.From)

		if e := validateBlock(run, idx, s.row, b.generation); e != nil {
			return e
		}

		b.podsEnd = run.PodsEnd
		b.runs = append(b.runs, run)
		return nil
	})
	if err != nil {
		return nil, err
	}
	tail := b.baseRows
	if len(b.runs) > 0 {
		tail = b.runs[len(b.runs)-1].End
	}
	indexRetired, e := retiredBytes(b.idx)
	if e != nil {
		return nil, e
	}
	if b.baseRows != s.catalog.RetiredRows[s.binding][branch] || indexRetired != b.baseRows*IndexRowSize {
		return nil, fmt.Errorf("index retirement boundary mismatch")
	}
	if err = b.idx.Truncate(headerSize + int64(tail-b.baseRows)*int64(s.row.Width)); err != nil {
		return nil, err
	}
	if err = b.pods.Truncate(int64(b.podsEnd - b.podsRetired)); err != nil {
		return nil, err
	}
	if err = s.loadSummaries(b); err != nil {
		return nil, err
	}

	if err = b.mapIndex(); err != nil {
		return nil, err
	}
	if err = s.warmHot(b); err != nil {
		return nil, err
	}
	if err = s.openSwap(b); err != nil {
		return nil, err
	}
	if b.summaryPackedThrough > b.finalizedBefore {
		return nil, fmt.Errorf("calendar coverage exceeds finalized data")
	}
	if err = s.publishTime(b); err != nil {
		return nil, err
	}
	s.branches[branch] = b
	return b, syncDir(s.dir)
}

// Append validates and durably stages an already encoded PIL event. Identical
// retries are deduplicated, including IDs in historical overlapping runs.
