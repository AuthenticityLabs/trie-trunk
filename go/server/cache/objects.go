package cache

// Optional collection identity and revision lookup; no payload scan fallback.
import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

var ErrObjectIndexDisabled = errors.New("object lookup requires collection.idx (object_index=true)")

type ObjectLocation struct {
	Minor      uint32
	Generation [16]byte
	Row        uint64
	Revision   Value
}

func (s *Store) collectionIdentity() error {
	if s.layout.Object < 0 || s.layout.Definition.Fields[s.layout.Object].Type != "UUID" {
		return fmt.Errorf("collection index requires UUID object identity")
	}
	if s.layout.Revision < 0 {
		return fmt.Errorf("collection index requires unsigned revision_id")
	}
	switch s.layout.Definition.Fields[s.layout.Revision].Type {
	case "U8", "U16", "U32", "U64":
		return nil
	}
	return fmt.Errorf("collection index requires unsigned revision_id")
}

type revisionSpan struct{ Low, High uint64 }

type collectionRevision struct {
	Object   [16]byte
	Revision uint64
}

func (s *Store) collectionRevision(values []Value) (collectionRevision, error) {
	var key collectionRevision
	object, revision := values[s.layout.Object], values[s.layout.Revision]
	if object.State != Direct || len(object.Bytes) != 16 || revision.State != Direct || (len(revision.Bytes) == 0 || len(revision.Bytes) > s.layout.Widths[s.layout.Revision]) {
		return key, fmt.Errorf("collection write requires UUID id and unsigned revision_id")
	}
	copy(key.Object[:], object.Bytes)
	key.Revision = unsigned(revision.Bytes)
	if key.Revision > collectionMaxRevision {
		return key, fmt.Errorf("collection revision exceeds U24")
	}
	return key, nil
}
func (s *Store) checkCollectionWrite(b *branchStore, values []Value) (collectionRevision, error) {
	if values[s.layout.Revision].State == Absent {
		return collectionRevision{}, nil
	}
	key, err := s.collectionRevision(values)
	if err != nil {
		return key, err
	}
	if b.live.revision(key) {
		return key, fmt.Errorf("object revision already staged")
	}
	if g := s.config.group; g != nil {
		for _, other := range g.stores {
			if other == s || other.collection != s.collection {
				continue
			}
			if branch := other.branches[Write]; branch != nil {
				if branch.live.revision(key) {
					return key, fmt.Errorf("object revision already staged in definition minor %d", other.layout.Definition.Resource.Minor)
				}
			}
		}
	}
	if s.objects != nil {
		for binding := range s.objects.sources {
			n := s.layout.Definition.Resource
			name := fmt.Sprintf("%s/%s.v%d/%d", n.Service, n.Resource, n.Major, binding.Minor)
			if active, ok := s.catalog.Generations[name][Write]; ok && active != binding.Generation {
				continue
			}
			directory, directoryErr := s.collectionDirectory(binding)
			if directoryErr != nil {
				return key, directoryErr
			}
			duplicate := false
			err := s.objects.visit(key.Object[:], binding, func(rev uint64, address collectionAddress) {
				if _, e := directory.resolveReference(address); e != nil {
					return
				}
				if rev == key.Revision {
					duplicate = true
				}
			})
			if err != nil {
				return key, err
			}
			if duplicate {
				return key, fmt.Errorf("object revision already committed in definition minor %d", binding.Minor)
			}
		}
	}

	return key, nil
}

func (s *Store) openObjects() error {
	if !s.config.ObjectIndex {
		return nil
	}
	if e := s.collectionIdentity(); e != nil {
		return e
	}
	if g := s.config.group; g != nil && g.objects[s.collection] != nil {
		s.objects = g.objects[s.collection]
		return nil
	}
	f, e := os.OpenFile(filepath.Join(s.collection, "collection.idx"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	s.objects, e = loadObjectIndex(f)
	if e != nil {
		var ioError *os.PathError
		if errors.As(e, &ioError) {
			f.Close()
			return e
		}
		// Derived current-format damage is rebuilt from canonical committed writes.
		// Canonical writes are authoritative; no old-format reader is provided.
		if e = f.Truncate(0); e != nil {
			f.Close()
			return e
		}
		if e = f.Sync(); e != nil {
			f.Close()
			return e
		}
		s.objects = newObjectIndex(f)
	}
	if g := s.config.group; g != nil {
		g.objects[s.collection] = s.objects
	}
	return nil
}

// The optional index is derived. A crash after write.journal commits but before this
// append is recovered by backfilling the missing source rows, without duplicating
// payloads or accepting an object posting as evidence of a committed write.
func (s *Store) indexObjects(b *branchStore) error {
	if !s.config.ObjectIndex || s.objects == nil || b.branch != Write {
		return nil
	}
	minor := s.layout.Definition.Resource.Minor
	through := s.objects.through[objectBinding{minor, b.generation}]
	tail := b.tail()
	if through > tail {
		return fmt.Errorf("object index ahead of source")
	}
	key := objectBinding{minor, b.generation}
	width := byte(s.layout.Widths[s.layout.Revision])
	if through < b.baseRows {
		if e := s.objects.append(key, width, through, b.baseRows, nil); e != nil {
			return e
		}
		through = b.baseRows
	}
	if through == tail {
		return nil
	}
	reader := populationReader{}
	var frame Frame
	for through < tail {
		end := min(through+1024, tail)
		var entries []collectionPosting
		for n := b.nextRecord(through); n < end; n = b.nextRecord(n + 1) {
			raw, e := reader.read(b, n, s.row)
			if e != nil {
				return e
			}
			if e = s.layout.identityFrame(raw, &frame); e != nil {
				return e
			}
			object := frame.Values[s.layout.Object]
			revision := frame.Values[s.layout.Revision]
			if revision.State == Absent {
				continue
			}
			if object.State != Direct || len(object.Bytes) != 16 || revision.State != Direct || (len(revision.Bytes) == 0 || len(revision.Bytes) > int(width)) {
				return fmt.Errorf("collection write requires UUID id and unsigned revision_id")
			}
			var id [16]byte
			copy(id[:], object.Bytes)
			entries = append(entries, collectionPosting{id, unsigned(revision.Bytes), b.eventReference(n)})
		}
		if e := s.objects.append(key, width, through, end, entries); e != nil {
			return e
		}
		through = end
	}

	return nil
}

// ObjectLocations spans definition minors, but only committed writes enter the
// index. Ordering is event-time order in History; revision values are unsigned. Consumers can stop at an exact source revision after resolving rows.
func (s *Store) ObjectLocations(id Value) ([]ObjectLocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.objectLocations(id)
}

// objectLocations requires s.mu and never falls back to scanning event data.
func (s *Store) objectLocations(id Value) ([]ObjectLocation, error) {
	s.waitSealing()
	if s.layout.Object >= 0 {
		id = s.layout.identityValue(s.layout.Object, id)
	}
	if e := s.check(); e != nil {
		return nil, e
	}
	if !s.config.ObjectIndex || s.objects == nil {
		return nil, ErrObjectIndexDisabled
	}
	if e := s.validateObject(id); e != nil {
		return nil, e
	}
	if b := s.branches[Write]; b != nil {
		if e := s.flush(b); e != nil {
			return nil, s.fail(e)
		}
		if e := s.indexObjects(b); e != nil {
			return nil, s.fail(e)
		}
	}
	var out []ObjectLocation
	for key, source := range s.objects.sources {
		n := s.layout.Definition.Resource
		binding := fmt.Sprintf("%s/%s.v%d/%d", n.Service, n.Resource, n.Major, key.Minor)
		if active, ok := s.catalog.Generations[binding][Write]; ok && active != key.Generation {
			continue
		}
		if key.Minor == n.Minor {
			if b := s.branches[Write]; b == nil || b.generation != key.Generation {
				continue
			}
		}
		width := int(source.Width)
		directory, e := s.collectionDirectory(key)
		if e != nil {
			return nil, e
		}
		err := s.objects.visit(id.Bytes, key, func(rev uint64, a collectionAddress) {
			if row, e := directory.resolveReference(a); e == nil {
				out = append(out, ObjectLocation{key.Minor, key.Generation, row, Uint(rev, width)})
			}
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Minor != out[j].Minor {
			return out[i].Minor < out[j].Minor
		}
		return out[i].Row < out[j].Row
	})
	return out, nil
}

// Object lookup resolves its own index references. Time bounds filter those
// records directly, never requiring temporal candidate enumeration.
func (s *Store) objectRows(b *branchStore, id Value, from, to time.Time) ([]uint64, error) {
	if !s.config.ObjectIndex || s.objects == nil {
		return nil, ErrObjectIndexDisabled
	}
	if e := s.validateObject(id); e != nil {
		return nil, e
	}
	if rows, complete := b.residentObjectRows(id.Bytes, from, to, s.config.Now()); complete {
		return rows, nil
	}
	key := objectBinding{s.layout.Definition.Resource.Minor, b.generation}
	var selected []uint64
	lo, hi := timeBound(from), timeBound(to)
	var invalid error
	err := s.objects.visit(id.Bytes, key, func(_ uint64, a collectionAddress) {
		row, e := b.time.resolveReference(a)
		if e != nil {
			return
		}
		ms := b.hourOrigin() + int64(a.Hour)*TimeHourMS + indexDelta(b.row(row))
		if ms >= lo && ms < hi {
			selected = append(selected, row)
		}
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i] < selected[j] })
	return selected, invalid
}

// History merges the current minor's object postings and durable pending writes.
// ProvisionalRow marks pending events. Cross-minor callers use Endpoint.History.
func (s *Store) History(ctx context.Context, id Value, from, to time.Time, visit func(uint64, []byte) error) error {
	s.mu.Lock()
	if e := s.check(); e != nil {
		s.mu.Unlock()
		return e
	}
	enabled, role := s.config.ObjectIndex, s.layout.Definition.Resource.Roles.Object
	s.mu.Unlock()
	if !enabled {
		return ErrObjectIndexDisabled
	}
	return s.searchView(ctx, Query{Branch: Write, From: from, To: to, Equals: map[string]Value{role: id}}, visit, s.assembledReader(true), true)
}
