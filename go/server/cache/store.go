package cache

// Store ownership and lifecycle. Writers and publication remain serialized by mu.
import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

type Config struct {
	sealHook            func(string) error // deterministic publication/failure probes in tests
	indexCapacity       uint64             // test-only capacity boundary; production uses all U32 slots
	group               *storeGroup
	Root, Cache, Tenant string
	Layout              *Layout
	ObjectIndex         bool
	BatchRecords        int
	BatchBytes          int
	Sealer              *SealScheduler // nil shares the process-wide single worker
	LiveIndexRecords    int            // maximum resident/staged identities per branch; default 1 million
	NativeMemoryBytes   uint64         // native payload budget per definition; default 1 GiB
	Now                 func() time.Time
}
type Catalog struct {
	RetiredRows         map[string]map[Branch]uint64 `pil:",omitempty"`
	Format              uint32
	Cache, Tenant       string
	Definitions         map[string][32]byte
	Generations         map[string]map[Branch][16]byte
	GenerationSequences map[string]map[Branch]uint64 `pil:",omitempty"`
	Preferred           map[string][32]byte          `pil:",omitempty"`
}
type pending struct {
	id    [16]byte
	frame []byte
}
type branchStore struct {
	memory                           map[int64]*nativeArena
	baseRows, podsRetired, memberEnd uint64
	blockBase                        uint32
	branch                           Branch
	layout                           *Layout
	generation                       [16]byte
	idx, pods, journal, sum          *os.File
	swap                             *os.File
	sealingBefore                    int64
	finalizedBefore                  int64
	rows                             []byte
	mapping                          []byte
	runs                             []Run
	time                             timeDirectory
	timeAnchor                       *TimeAnchor
	timeDirty                        bool
	live                             *nativeLive
	liveViews                        map[string]liveViewSnapshot
	pendingVersion                   uint64
	queryNewest, queryLateFrom       int64
	queryObservedThrough             int64
	queryLateVersion                 uint64
	provisionalRecords               map[int64]uint32
	pendingBytes                     int
	podsEnd, sumEnd                  uint64
	summaries                        []Summary
	accumulators                     map[int64]*summaryAccumulator
	summaryDays                      map[int64]summaryDayLocation
	summaryPackedThrough             int64
	calendarRollups                  []CalendarRollup
}
type Store struct {
	summaryExports     map[string][]byte
	summaryExportOrder []string
	summaryExportBytes int
	sealing            chan struct{} // writer-lock protected; maintenance waits without holding mu
	memory             *nativeMemory
	capacityClosed     bool
	transforms         *TransformConfig
	searchContext      context.Context
	searchCancel       context.CancelFunc
	searchWG           sync.WaitGroup
	searchTouched      bool
	searchOnce         sync.Once
	searchGates        [5]searchSerial
	closeDone          chan struct{}

	mu                    *sync.Mutex
	config                Config
	requestedObjectIndex  bool
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
	branchPolicies        map[Branch]Policy
	catalog               Catalog
	binding               string
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
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waitSealing()
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
	for _, a := range b.memory {
		a.close()
	}
	b.memory = nil
	b.live.close()
	b.live = nil
	b.liveViews = nil
	e := b.unmapIndex()
	for _, f := range []*os.File{b.idx, b.pods, b.journal, b.sum, b.swap} {
		if f != nil {
			e = errors.Join(e, f.Close())
		}
	}
	return e
}
func (s *Store) release() error {
	defer s.memory.close()
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
	s.waitSealing()
	if s.closed {
		done := s.closeDone
		s.mu.Unlock()
		if done != nil {
			<-done
		}
		return nil
	}
	s.closeDone = make(chan struct{})
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
	if s.searchCancel != nil {
		s.searchCancel()
	}
	touched := s.searchTouched
	s.mu.Unlock()
	s.finishSearch(touched)
	s.mu.Lock()
	e = errors.Join(e, s.release())
	s.mu.Unlock()
	close(s.closeDone)
	return e
}
func (s *Store) Generation(branch Branch) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b := s.branches[branch]; b != nil {
		return hex.EncodeToString(b.generation[:])
	}
	return ""
}

// Called with mu held. Only maintenance waits; reads continue against the
// published disk prefix plus the native pending set during a seal.
func (s *Store) waitSealing() {
	for s.sealing != nil {
		done := s.sealing
		s.mu.Unlock()
		<-done
		s.mu.Lock()
	}
}
