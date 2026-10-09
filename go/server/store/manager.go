package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"trie-trunk/go/codec"
)

type Config struct {
	recoverOnly                                             bool // Offline migration validates/recover tails without publishing new summaries.
	Tap                                                     string
	Root                                                    string
	MaxPartitions, MaxSchemas, MaxNodes, MaxGroups, MaxRuns int
	BatchRecords, BatchBytes                                int
	MaxAge, FutureSkew                                      time.Duration
	Now                                                     func() time.Time
}

func (c Config) defaults() (Config, error) {
	if c.Root == "" {
		return c, fmt.Errorf("storage root required")
	}
	if c.Tap == "" {
		c.Tap = "0"
	}
	if err := ValidateTap(c.Tap); err != nil {
		return c, err
	}
	if c.MaxPartitions == 0 {
		c.MaxPartitions = 32
	}
	if c.MaxSchemas == 0 {
		c.MaxSchemas = 128
	}
	if c.MaxNodes == 0 {
		c.MaxNodes = 65536
	}
	if c.MaxGroups == 0 {
		c.MaxGroups = 16384
	}
	if c.MaxRuns == 0 {
		c.MaxRuns = 100000
	}
	if c.BatchRecords == 0 {
		c.BatchRecords = 1024
	}
	if c.BatchBytes == 0 {
		c.BatchBytes = 4 << 20
	}
	if c.MaxAge == 0 {
		c.MaxAge = 30 * 24 * time.Hour
	}
	if c.FutureSkew == 0 {
		c.FutureSkew = time.Minute
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.MaxPartitions < 1 || c.MaxSchemas < 1 || c.MaxNodes < 1 || uint64(c.MaxNodes) > 1<<32 || c.MaxGroups < 1 || c.MaxGroups > (maxBlockSize-14)/36 || c.MaxRuns < 1 || c.BatchRecords < 1 || c.BatchBytes < codec.MaxFramePayload+indexRowSize || c.BatchBytes > maxBlockSize/2 || c.MaxAge < 0 || c.FutureSkew < 0 {
		return c, fmt.Errorf("invalid storage limits")
	}
	return c, nil
}

type Manager struct {
	active     map[[32]byte]map[string]scope
	mu         sync.Mutex
	config     Config
	lock       *os.File
	schemas    map[[32]byte]*schema
	partitions map[string]*partition
	clock      uint64
	known      map[[32]byte]map[string]scope
	maximum    map[string]codec.UUIDv7
	closed     bool
	failed     error
}

func Open(c Config) (m *Manager, err error) {
	c, err = c.defaults()
	if err != nil {
		return nil, err
	}
	if err = checkTapDirectory(c.Root, c.Tap); err != nil {
		return nil, err
	}
	if err = mkdirDurable(filepath.Join(c.Root, c.Tap)); err != nil {
		return nil, err
	}
	lock, err := lockRoot(filepath.Join(c.Root, c.Tap))
	if err != nil {
		return nil, err
	}
	m = &Manager{active: make(map[[32]byte]map[string]scope), config: c, lock: lock, known: make(map[[32]byte]map[string]scope), maximum: make(map[string]codec.UUIDv7), schemas: make(map[[32]byte]*schema), partitions: make(map[string]*partition)}
	defer func() {
		if err != nil {
			lock.Close()
		}
	}()
	catalogs, err := ReadCatalogs(context.Background(), c.Root, c.Tap)
	if err != nil {
		return nil, err
	}
	for _, catalog := range catalogs {
		d, rev := catalog.Definition, catalog.Revision
		s, e := compile(d, rev)
		if e != nil {
			return nil, e
		}
		if len(m.schemas) >= c.MaxSchemas {
			return nil, ErrLimit
		}
		m.schemas[rev.Digest] = s

		tr, e := acquireTrie(c, s)
		if e != nil {
			return nil, e
		}

		e = filepath.WalkDir(s.directory(c.Root, c.Tap), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || entry.Name() != s.filename("idx") {
				return nil
			}
			rel, e := filepath.Rel(s.directory(c.Root, c.Tap), filepath.Dir(path))
			if e != nil {
				return e
			}
			var sc scope
			if s.modern() {
				sc, _, e = readScope(filepath.Dir(path))
				sc.Dir = rel
			} else {
				var day time.Time
				day, e = parseDay(rel)
				sc = scope{Start: day, End: day.AddDate(0, 0, 1), Dir: rel}
			}
			if e != nil {
				return fmt.Errorf("invalid partition %s: %w", rel, e)
			}
			p, e := openScopedPartition(c.Root, s, sc, c)
			if e != nil {
				return e
			}
			m.remember(s, p)
			e = p.flush(c.Now())
			return errors.Join(e, p.closeFiles())
		})
		e = errors.Join(e, tr.release())
		if e != nil {
			return nil, e
		}
	}
	if err = m.writeRegistry(); err != nil {
		return nil, err
	}
	if err = m.publishAllStatistics(c.Now()); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) check() error {
	if m.closed {
		return ErrClosed
	}
	if m.failed != nil {
		return fmt.Errorf("store requires reopen after I/O failure: %w", m.failed)
	}
	return nil
}

func (m *Manager) fail(e error) error {
	if e != nil && !errors.Is(e, ErrLimit) && !errors.Is(e, ErrInvalid) {
		m.failed = e
	}
	return e
}

func (m *Manager) Revision(version uint32) (codec.Revision, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var found codec.Revision
	matched := false
	for _, s := range m.schemas {
		if s.revision.Version != version {
			continue
		}
		if matched {
			return codec.Revision{}, false
		}
		found, matched = s.revision, true
	}
	return found, matched
}

func (m *Manager) FindRevision(rev codec.Revision) (codec.Revision, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.schemas[rev.Digest]
	if !ok || s.revision != rev {
		return codec.Revision{}, false
	}
	return s.revision, true
}

func (m *Manager) Install(rev codec.Revision, data []byte) error {
	if len(data) > codec.MaxFramePayload {
		return codec.ErrFrameSize
	}
	d, e := codec.ParseDefinition(data, rev)
	if e != nil {
		return fmt.Errorf("%w: schema: %v", ErrInvalid, e)
	}
	s, e := compile(d, rev)
	if e != nil {
		if errors.Is(e, ErrLimit) {
			return e
		}
		return fmt.Errorf("%w: schema: %v", ErrInvalid, e)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.install(s, data)
}

func (m *Manager) install(s *schema, data []byte) error {
	if e := m.check(); e != nil {
		return e
	}
	rev := s.revision
	for _, old := range m.schemas {
		if old.definition.Object != s.definition.Object || old.revision.Version != rev.Version {
			continue
		}
		if old.revision != rev {
			return ErrConflict
		}
		return nil
	}
	if len(m.schemas) >= m.config.MaxSchemas {
		return ErrLimit
	}
	if e := s.checkDirectory(m.config.Root, m.config.Tap); e != nil {
		return e
	}
	if e := installNamedMap(s.directory(m.config.Root, m.config.Tap), s.filename("map"), data); e != nil {
		return m.fail(e)
	}
	m.schemas[rev.Digest] = s
	if s.definition.Object != "" {
		tr, err := acquireTrie(m.config, s)
		if err != nil {
			return m.fail(err)
		}
		if err = tr.release(); err != nil {
			return m.fail(err)
		}
	}
	if err := m.writeRegistry(); err != nil {
		return m.fail(err)
	}
	return m.fail(m.publishStatistics(s, m.config.Now(), true))
}

func (m *Manager) partition(s *schema, day time.Time) (*partition, error) {
	return m.partitionAt(s, scope{Start: day, End: day.AddDate(0, 0, 1), Dir: dayPath(day)})
}
func (m *Manager) partitionAt(s *schema, sc scope) (*partition, error) {
	key := fmt.Sprintf("%x/%s", s.revision.Digest, sc.Dir)
	m.clock++
	if p, ok := m.partitions[key]; ok {
		p.touched = m.clock
		return p, nil
	}
	if len(m.partitions) >= m.config.MaxPartitions {
		var oldest *partition
		var name string
		for k, p := range m.partitions {
			if oldest == nil || p.touched < oldest.touched {
				oldest = p
				name = k
			}
		}
		if e := oldest.flush(m.config.Now()); e != nil {
			return nil, m.fail(e)
		}
		m.remember(oldest.schema, oldest)
		if e := oldest.closeFiles(); e != nil {
			return nil, m.fail(e)
		}
		delete(m.partitions, name)
	}
	p, e := openScopedPartition(m.config.Root, s, sc, m.config)
	if e != nil {
		return nil, m.fail(e)
	}
	p.touched = m.clock
	m.partitions[key] = p
	m.remember(s, p)
	return p, nil
}

func (m *Manager) Append(rev codec.Revision, data []byte) (duplicate bool, err error) {
	m.mu.Lock()
	if err = m.check(); err != nil {
		m.mu.Unlock()
		return
	}
	s, ok := m.schemas[rev.Digest]
	m.mu.Unlock()
	if !ok || s.revision != rev {
		return false, ErrConflict
	}
	r, err := s.decode(data)
	if err != nil {
		return false, err
	}
	now := m.config.Now()
	at := time.UnixMilli(millis(r.Id))
	if at.Before(now.Add(-m.config.MaxAge)) || at.After(now.Add(m.config.FutureSkew)) {
		return false, fmt.Errorf("%w: id time outside ingestion window", ErrInvalid)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err = m.check(); err != nil {
		return
	}
	var p *partition
	if s.modern() {
		tr, e := acquireTrie(m.config, s)
		if e != nil {
			return false, m.fail(e)
		}
		r, e = tr.prepare(r, m.config)
		owner := s.owner(tr, r.Leaf)
		if e != nil {
			tr.release()
			return false, m.fail(e)
		}
		sc, e := m.routeScope(s, r.Id, owner)
		if e != nil {
			tr.release()
			return false, e
		}
		// Identity remains tap/schema/day-wide, even if a duplicate changes branch.
		duplicate, e = m.containsScoped(s, r.Id, sc)
		if e != nil || duplicate {
			tr.release()
			return duplicate, m.fail(e)
		}
		p, err = m.partitionAt(s, sc)
		releaseErr := tr.release()
		if err == nil {
			err = releaseErr
		}
	} else {
		p, err = m.partition(s, dayOf(r.Id))
	}
	if err != nil {
		return false, err
	}
	duplicate, err = p.append(r, now)
	if err == nil {
		m.rememberID(s, r.Id)
	}
	return duplicate, m.fail(err)
}

func (m *Manager) Flush() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.check(); e != nil {
		return e
	}
	for _, p := range m.partitions {
		if e := p.flush(m.config.Now()); e != nil {
			return m.fail(e)
		}
		m.remember(p.schema, p)
	}
	// Closed descriptors do not postpone maturity indefinitely.
	for version, active := range m.active {
		for _, sc := range active {
			if sc.End.After(m.config.Now().Add(-matureDelay)) {
				continue
			}
			p, e := m.partitionAt(m.schemas[version], sc)
			if e != nil {
				return m.fail(e)
			}
			if e = p.flush(m.config.Now()); e != nil {
				return m.fail(e)
			}
			m.remember(p.schema, p)
		}
	}
	return m.fail(m.publishAllStatistics(m.config.Now()))
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	var errs []error
	if m.failed == nil {
		for _, p := range m.partitions {
			errs = append(errs, p.flush(m.config.Now()))
			m.remember(p.schema, p)
		}
		if errors.Join(errs...) == nil {
			errs = append(errs, m.publishAllStatistics(m.config.Now()))
		}
	} else {
		errs = append(errs, m.failed)
	}
	for _, p := range m.partitions {
		errs = append(errs, p.closeFiles())
	}
	errs = append(errs, m.lock.Close())
	m.closed = true
	return errors.Join(errs...)
}

func (m *Manager) Scan(rev codec.Revision, day time.Time, visit func(codec.Record) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.check(); e != nil {
		return e
	}
	s, ok := m.schemas[rev.Digest]
	if !ok || s.revision != rev {
		return ErrConflict
	}
	if s.modern() {
		return m.scanScoped(s, day, visit)
	}
	p, e := m.partition(s, day.UTC().Truncate(24*time.Hour))
	if e != nil {
		return e
	}
	return scanPartition(p, s, visit)
}
func scanPartition(p *partition, s *schema, visit func(codec.Record) error) error {
	var e error
	for _, run := range p.runs {
		b := make([]byte, run.End-run.Offset-8)
		if _, e = p.idx.ReadAt(b, run.Offset+8); e != nil {
			return e
		}
		rows, e := p.decodeRun(b)
		if e != nil {
			return e
		}
		pop, e := p.readPopRun(rows)
		if e != nil {
			return e
		}
		for _, r := range rows {
			fields, e := codec.Decode(pop[r.Start-rows[0].Start:r.End-rows[0].Start], codec.CBOR)
			if e != nil {
				return e
			}
			fields[s.id] = r.Id
			for leaf := r.Leaf; leaf != 0; leaf = p.trie.nodes[leaf].Parent {
				n := p.trie.nodes[leaf]
				var value uint64
				if e = decoder.Unmarshal(n.Value, &value); e != nil {
					return e
				}
				fields[n.Key] = value
			}
			if e = visit(fields); e != nil {
				return e
			}
		}
	}
	return nil
}

func (m *Manager) Summaries(rev codec.Revision, day time.Time) ([]Summary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.check(); e != nil {
		return nil, e
	}
	s, ok := m.schemas[rev.Digest]
	if !ok || s.revision != rev {
		return nil, ErrConflict
	}
	if s.modern() {
		return m.summariesScoped(s, day)
	}
	p, e := m.partition(s, day.UTC().Truncate(24*time.Hour))
	if e != nil {
		return nil, e
	}
	var result []Summary
	e = scan(p.sum, func(_, _ int64, b []byte) error {
		v, e := decodeSummary(bytes.Clone(b))
		if e == nil {
			result = append(result, v)
		}
		return e
	})
	return result, e
}
