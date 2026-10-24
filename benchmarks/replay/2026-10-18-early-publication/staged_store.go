package cache

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Config struct {
	group               *storeGroup
	Root, Cache, Tenant string
	Layout              *Layout
	ObjectIndex         bool
	BatchRecords        int
	BatchBytes          int
	Now                 func() time.Time
}
type Catalog struct {
	Format              uint32
	Cache, Tenant       string
	Definitions         map[string][32]byte
	Generations         map[string]map[Branch][16]byte
	GenerationSequences map[string]map[Branch]uint64 `cbor:",omitempty"`
	Preferred           map[string][32]byte          `cbor:",omitempty"`
}
type pending struct {
	id    [16]byte
	frame []byte
}
type branchStore struct {
	branch             Branch
	generation         [16]byte
	idx, pop, tri, sum *os.File
	swap               *os.File
	finalizedBefore    int64
	rows               []byte
	mapping            []byte
	runs               []Run
	time               timeTrie
	pending            []pending
	pendingIDs         map[[16]byte][]byte
	pendingObjects     map[string][]pending
	hot                hotFrames
	liveViews          map[string]liveViewSnapshot
	pendingVersion     uint64
	pendingUnsorted    bool
	pendingBytes       int
	popEnd, sumEnd     uint64
	summaries          []Summary
}
type Store struct {
	mu                    *sync.Mutex
	config                Config
	layout                *Layout
	row                   RowLayout
	root, dir, collection string
	lock, shared          *os.File
	audit                 *os.File
	trie                  sharedTrie
	branches              map[Branch]*branchStore
	location              *time.Location
	scratch               Frame
	closed                bool
	failed                error
	objects               *objectIndex
	state                 *objectState
	policy                Policy
	catalog               Catalog
	binding               string
}

// Open takes one writer lease for this cache. A definition remains immutable;
// experiments must use a new minor instead of reinterpreting retained bytes.
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
	s = &Store{mu: &sync.Mutex{}, config: c, layout: l, row: l.RowLayout(), trie: newTrie(), branches: map[Branch]*branchStore{}}
	s.location, _ = time.LoadLocation(l.Definition.Nuts.Timezone)
	s.root = filepath.Join(c.Root, c.Cache)
	n := l.Definition.Nuts
	s.collection = filepath.Join(s.root, n.Domain, fmt.Sprintf("%s.v%d", n.Collection, n.Major))
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
	if err = s.removeSwapTemps(); err != nil {
		return nil, err
	}
	path := filepath.Join(s.root, "collections.map")
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
	key := fmt.Sprintf("%s/%s.v%d/%d", n.Domain, n.Collection, n.Major, n.Minor)
	s.binding = key
	if digest, ok := catalog.Definitions[key]; ok && digest != l.Digest {
		return nil, fmt.Errorf("immutable definition conflict")
	}
	for binding := range catalog.Definitions {
		p := splitBinding(binding)
		if p == nil {
			return nil, fmt.Errorf("invalid catalog binding")
		}
		if p.domain != n.Domain || p.collection != n.Collection || p.major != n.Major || p.minor == n.Minor {
			continue
		}
		raw, e := os.ReadFile(filepath.Join(s.root, binding, "nuts.map"))
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
	if err = ensureMap(filepath.Join(s.dir, "nuts.map"), l.Bytes); err != nil {
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
	if err = replace(filepath.Join(s.root, "collections.tri"), b); err != nil {
		return nil, err
	}
	s.shared, err = openFile(filepath.Join(s.dir, "nuts.tri"), "NUTSTRI6", l.Digest, [16]byte{}, 0)
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
		if _, e = os.Stat(filepath.Join(s.dir, s.prefix(branch)+".tri")); e == nil {
			if _, err = s.openBranch(branch); err != nil {
				return nil, err
			}
		} else if !os.IsNotExist(e) {
			return nil, e
		} else if _, required := s.catalog.Generations[s.binding][branch]; required {
			return nil, fmt.Errorf("catalog-selected branch is missing")
		}
	}
	if b := s.branches[Write]; b != nil {
		if err = s.indexObjects(b); err != nil {
			return nil, err
		}
	}
	return s, nil
}
func (s *Store) check() error {
	if s.closed {
		return fmt.Errorf("cache closed")
	}
	if s.state != nil && s.state.failed != nil {
		return s.state.failed
	}
	return s.failed
}
func (s *Store) fail(e error) error {
	if e != nil {
		s.failed = e
	}
	return e
}
func (s *Store) openBranch(branch Branch) (b *branchStore, err error) {
	if !branch.Valid() {
		return nil, fmt.Errorf("invalid branch")
	}
	if b = s.branches[branch]; b != nil {
		return b, nil
	}
	b = &branchStore{branch: branch, time: newTimeTrie(s.policy.Replication.hours()), pendingIDs: map[[16]byte][]byte{}, popEnd: headerSize, sumEnd: headerSize}
	base := filepath.Join(s.dir, s.prefix(branch))
	raw, e := os.ReadFile(base + ".tri")
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
	if b.tri, err = openFile(base+".tri", "TIMETRI6", s.layout.Digest, b.generation, s.row.Width); err != nil {
		return nil, err
	}
	if b.idx, err = openFile(base+".idx", "NUTSIDX6", s.layout.Digest, b.generation, s.row.Width); err != nil {
		return nil, err
	}
	if b.pop, err = openFile(base+".pop", "NUTSPOP6", s.layout.Digest, b.generation, 0); err != nil {
		return nil, err
	}
	if branch.Aggregates() && len(s.layout.Definition.Nuts.Aggregations) > 0 {
		if b.sum, err = openFile(base+".sum", "NUTSSUM6", s.layout.Digest, b.generation, 0); err != nil {
			return nil, err
		}
	}
	err = records(b.tri, func(raw []byte) error {
		var commit Commit
		if e := decoder.Unmarshal(raw, &commit); e != nil {
			return e
		}
		run := commit.Run
		var tail uint64
		if len(b.runs) > 0 {
			tail = b.runs[len(b.runs)-1].End
		}
		if run.Start != tail || run.End <= tail || run.End-run.Start > uint64(MaxFrame*8/s.row.Width) || run.PopStart != b.popEnd || run.PopEnd < run.PopStart || run.TrieSize > len(s.trie.nodes) || run.TrieSize < 1 {
			return fmt.Errorf("invalid committed run")
		}
		idx, e := readBytes(b.idx, headerSize+run.Start*uint64(s.row.Width), headerSize+run.End*uint64(s.row.Width))
		if e != nil {
			return e
		}
		pop, e := readBytes(b.pop, run.PopStart, run.PopEnd)
		if e != nil {
			return e
		}
		if crc32.Checksum(idx, checksum) != run.IndexCRC || crc32.Checksum(pop, checksum) != run.PopCRC {
			return fmt.Errorf("committed data checksum mismatch")
		}
		start := run.PopStart
		for i := 0; i < len(idx); i += s.row.Width {
			row := idx[i : i+s.row.Width]
			ms := eventTime(row)
			if i > 0 && bytes.Compare(idx[i-s.row.Width:i-s.row.Width+16], row[:16]) > 0 {
				return fmt.Errorf("unsorted run")
			}
			if ms < run.MinMS || ms > run.MaxMS {
				return fmt.Errorf("run bounds mismatch")
			}
			end := order.Uint64(row[s.row.End:])
			if end <= start || end-start > MaxFrame || end > run.PopEnd || int(order.Uint16(row[s.row.Reference:])) >= run.TrieSize {
				return fmt.Errorf("invalid row bounds")
			}
			start = end
			b.time.append(ms, run.Start+uint64(i/s.row.Width), s.location)
		}
		if start != run.PopEnd {
			return fmt.Errorf("population final end does not match committed run")
		}
		if commit.SumEnd < b.sumEnd {
			return fmt.Errorf("invalid summary end")
		}
		b.sumEnd = commit.SumEnd
		b.popEnd = run.PopEnd
		b.runs = append(b.runs, run)
		return nil
	})
	if err != nil {
		return nil, err
	}
	var tail uint64
	if len(b.runs) > 0 {
		tail = b.runs[len(b.runs)-1].End
	}
	if err = b.idx.Truncate(headerSize + int64(tail)*int64(s.row.Width)); err != nil {
		return nil, err
	}
	if err = b.pop.Truncate(int64(b.popEnd)); err != nil {
		return nil, err
	}
	if b.sum != nil {
		stat, e := b.sum.Stat()
		if e != nil {
			return nil, e
		}
		if uint64(stat.Size()) < b.sumEnd {
			return nil, fmt.Errorf("truncated committed summaries")
		}
		if err = b.sum.Truncate(int64(b.sumEnd)); err != nil {
			return nil, err
		}
		if err = records(b.sum, func(raw []byte) error {
			var summary Summary
			if e := decodeSummary(raw, &summary); e != nil {
				return e
			}
			b.summaries = append(b.summaries, summary)
			return nil
		}); err != nil {
			return nil, err
		}
		if len(b.summaries) != len(b.runs) {
			return nil, fmt.Errorf("missing committed summaries")
		}
		for i, summary := range b.summaries {
			if summary.From != b.runs[i].Start || summary.Through != b.runs[i].End {
				return nil, fmt.Errorf("summary/source watermark mismatch")
			}
		}
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
	s.branches[branch] = b
	return b, syncDir(s.dir)
}

// Append validates and durably stages an already encoded PIL event. Identical
// retries are deduplicated, including IDs in historical overlapping runs.
// Row IDs are assigned only after the three-cycle finalization boundary.
func (s *Store) Append(branch Branch, raw []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendStaged(branch, raw, true)
}

// AppendBatch acknowledges a durable prefix with one swap sync. The returned
// count includes idempotent duplicates, and stops before the first rejection.
func (s *Store) AppendBatch(branch Branch, frames [][]byte) (count int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, raw := range frames {
		if _, err = s.appendStaged(branch, raw, false); err != nil {
			break
		}
		count++
	}
	if count>0 && BenchmarkBeforeSwapSync!=nil {
  if e:=BenchmarkBeforeSwapSync(frames[:count]);e!=nil{return count,e}
 }
	if b := s.branches[branch]; b != nil && b.swap != nil {
		if e := b.swap.Sync(); e != nil {
			return 0, s.fail(e)
		}
	}
	return count, err
}
func (s *Store) appendStaged(branch Branch, raw []byte, sync bool) (bool, error) {
	if e := s.check(); e != nil {
		return false, e
	}
	if e := s.layout.Scan(raw, &s.scratch); e != nil {
		return false, e
	}
	id, e := eventID(s.scratch.Values[s.layout.Event])
	if e != nil {
		return false, e
	}
	b, e := s.openBranch(branch)
	if e != nil {
		return false, s.fail(e)
	}
	if old, ok := b.pendingIDs[id]; ok {
		if bytes.Equal(old, raw) {
			return true, nil
		}
		return false, fmt.Errorf("conflicting event_id")
	}
	if duplicate, e := s.committedDuplicate(b, id, raw); duplicate || e != nil {
		return duplicate, e
	}
	if eventTime(id[:]) < b.finalizedBefore || len(b.rows) > 0 && bytes.Compare(id[:], b.rows[len(b.rows)-s.row.Width:len(b.rows)-s.row.Width+16]) <= 0 {
		return false, s.rejectLate(b, id, raw)
	}
	if e = s.stagingCapacity(len(raw)); e != nil {
		return false, e
	}
	// Receipt is acknowledged only after the complete frame is on stable storage.
	if e = appendRecord(b.swap, raw); e == nil && sync {
		e = b.swap.Sync()
	}
	if e != nil {
		return false, s.fail(e)
	}
	if e = s.invalidateReceived(branch, raw); e != nil {
		return false, s.fail(e)
	}
	copyFrame := append([]byte(nil), raw...)
	b.pending = append(b.pending, pending{id, copyFrame})
	b.pendingIDs[id] = copyFrame
	b.pendingBytes += len(raw)
	if e = s.rememberPending(b, pending{id, copyFrame}); e != nil {
		return false, s.fail(e)
	}
	return false, nil
}

func (s *Store) committedDuplicate(b *branchStore, id [16]byte, raw []byte) (bool, error) {
	for _, run := range b.runs {
		ms := eventTime(id[:])
		if ms < run.MinMS || ms > run.MaxMS {
			continue
		}
		at := lower(b.rows, s.row.Width, run.Start, run.End, ms)
		for at < run.End {
			row := b.rows[at*uint64(s.row.Width) : (at+1)*uint64(s.row.Width)]
			if eventTime(row) != ms {
				break
			}
			if equalID(row, id) {
				full, e := s.frame(b, at, nil)
				if e != nil {
					return false, e
				}
				if bytes.Equal(full, raw) {
					return true, nil
				}
				return false, fmt.Errorf("conflicting event_id")
			}
			at++
		}
	}
	return false, nil
}

type Commit struct {
	Run    Run    `cbor:"0,keyasint"`
	SumEnd uint64 `cbor:"1,keyasint"`
}

func (s *Store) commitBatch(b *branchStore, batch []pending) error {
	if len(batch) == 0 {
		return nil
	}
	sort.Slice(batch, func(i, j int) bool { return bytes.Compare(batch[i].id[:], batch[j].id[:]) < 0 })
	idx := make([]byte, 0, len(batch)*s.row.Width)
	size := 0
	for _, p := range batch {
		size += len(p.frame)
	}
	pop := make([]byte, 0, size)
	var frame Frame
	var encoded []byte
	startRow := uint64(len(b.rows) / s.row.Width)
	groups := map[aggregateKey]Aggregate{}
	for _, p := range batch {
		if e := s.layout.Scan(p.frame, &frame); e != nil {
			return e
		}
		v := frame.Values
		ref := s.trie.intern(s.layout, v)
		row := make([]byte, s.row.Width)
		copy(row, p.id[:])
		order.PutUint16(row[s.row.Reference:], ref)
		if b.sum != nil {
			key := aggregateKey{Period: eventTime(p.id[:]) / 300000 * 300000, Reference: ref}
			a := groups[key]
			var duration *uint64
			if s.layout.Duration >= 0 && v[s.layout.Duration].State == Direct {
				d := unsigned(v[s.layout.Duration].Bytes)
				duration = &d
			}
			if e := a.add(duration); e != nil {
				return e
			}
			groups[key] = a
		}
		v[s.layout.Event] = Value{}
		s.trie.remove(ref, v)
		var e error
		encoded, e = s.layout.Encode(encoded, v)
		if e != nil {
			return e
		}
		from := b.popEnd + uint64(len(pop))
		order.PutUint64(row[s.row.End:], from+uint64(len(encoded)))
		pop = append(pop, encoded...)
		idx = append(idx, row...)
	}
	if len(s.trie.nodes) > s.trie.persisted {
		raw, e := encoder.Marshal(s.trie.nodes[s.trie.persisted:])
		if e != nil {
			return e
		}
		if e = appendRecord(s.shared, raw); e != nil {
			return e
		}
		if e = s.shared.Sync(); e != nil {
			return e
		}
		s.trie.persisted = len(s.trie.nodes)
	}
	if _, e := b.pop.WriteAt(pop, int64(b.popEnd)); e != nil {
		return e
	}
	if e := b.pop.Sync(); e != nil {
		return e
	}
	if _, e := b.idx.WriteAt(idx, headerSize+int64(len(b.rows))); e != nil {
		return e
	}
	if e := b.idx.Sync(); e != nil {
		return e
	}
	run := Run{Start: startRow, End: startRow + uint64(len(batch)), PopStart: b.popEnd, PopEnd: b.popEnd + uint64(len(pop)), MinMS: eventTime(batch[0].id[:]), MaxMS: eventTime(batch[len(batch)-1].id[:]), IndexCRC: crc32.Checksum(idx, checksum), PopCRC: crc32.Checksum(pop, checksum), TrieSize: len(s.trie.nodes)}
	if b.sum != nil {
		summary := makeSummary(run, groups)
		raw, e := encodeSummary(summary)
		if e != nil {
			return e
		}
		if e = appendRecord(b.sum, raw); e != nil {
			return e
		}
		if e = b.sum.Sync(); e != nil {
			return e
		}
		stat, e := b.sum.Stat()
		if e != nil {
			return e
		}
		b.sumEnd = uint64(stat.Size())
		b.summaries = append(b.summaries, summary)
	}
	raw, e := encoder.Marshal(Commit{run, b.sumEnd})
	if e != nil {
		return e
	}
	if e = appendRecord(b.tri, raw); e != nil {
		return e
	}
	if e = b.tri.Sync(); e != nil {
		return e
	}
	for i := 0; i < len(idx); i += s.row.Width {
		b.time.append(eventTime(idx[i:]), startRow+uint64(i/s.row.Width), s.location)
	}
	if e = b.mapIndex(); e != nil {
		return e
	}
	b.runs = append(b.runs, run)
	b.popEnd = run.PopEnd
	return s.indexObjects(b)
}
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return e
	}
	for branch := Read; branch <= Trace; branch++ {
		if b := s.branches[branch]; b != nil {
			if e := s.flush(b); e != nil {
				return s.fail(e)
			}
		}
	}
	return nil
}
func (b *branchStore) close() error {
	e := b.unmapIndex()
	for _, f := range []*os.File{b.idx, b.pop, b.tri, b.sum, b.swap} {
		if f != nil {
			e = errors.Join(e, f.Close())
		}
	}
	return e
}
func (s *Store) release() error {
	var e error
	for _, b := range s.branches {
		e = errors.Join(e, b.close())
	}
	if s.shared != nil {
		e = errors.Join(e, s.shared.Close())
	}
	if s.audit != nil {
		e = errors.Join(e, s.audit.Close())
	}
	if s.config.group == nil && s.objects != nil && s.objects.file != nil {
		e = errors.Join(e, s.objects.file.Close())
	}
	if s.config.group == nil && s.state != nil {
		e = errors.Join(e, s.state.file.Close())
	}
	if s.lock != nil {
		e = errors.Join(e, s.lock.Close())
	}
	return e
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	e := s.failed
	if e == nil {
		for branch := Read; branch <= Trace; branch++ {
			if b := s.branches[branch]; b != nil {
				if e = s.flush(b); e != nil {
					break
				}
			}
		}
	}
	if e == nil {
		e = s.publishStatistics()
	}
	s.closed = true
	return errors.Join(e, s.release())
}
func (s *Store) Generation(branch Branch) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b := s.branches[branch]; b != nil {
		return hex.EncodeToString(b.generation[:])
	}
	return ""
}

// Benchmark-only observer; installed before requests and removed after completion.
var BenchmarkBeforeSwapSync func([][]byte) error
