package cache

// Pin index mappings and source watermarks for a consistent query snapshot.
import (
	"fmt"
	"os"
	"time"
)

type viewBinding struct {
	Retired    uint64
	Definition string
	Generation string
	Branch     Branch
	Tail       uint64
	Finalized  int64
	Live       string `json:",omitempty"`
}
type viewCursor struct {
	Query      string
	Scope      string
	From, To   string
	Expires    int64
	Sources    []viewBinding
	LastID     string
	LastSource int
	LastRow    uint64
}
type ViewReference struct {
	Stream      uint32               `json:"stream"`
	Row         uint64               `json:"row"`
	Key         string               `json:"key"`
	Cached      bool                 `json:"cached"`
	Provisional bool                 `json:"provisional,omitempty"`
	Native      *viewNativeReference `json:"native,omitempty"`
}
type viewNativeReference struct {
	From       int64  `json:"from"`
	To         int64  `json:"to"`
	Generation string `json:"generation"`
}
type ViewSummary struct {
	Statistics *ViewStatistics `json:"statistics,omitempty"`
	From       int64           `json:"from"`
	To         int64           `json:"to"`
	Step       int64           `json:"step"`
	Counts     []uint64        `json:"counts"`
	Errors     []uint64        `json:"errors"`
	Count      uint64          `json:"count"`
	Status     string          `json:"status"`
}
type ViewResult struct {
	Analytics        *ViewAnalytics  `json:"analytics,omitempty"`
	Buckets          []ViewBucket    `json:"buckets,omitempty"`
	Cursor           string          `json:"cursor"`
	Snapshot         string          `json:"snapshot"`
	SummaryVersion   string          `json:"summaryVersion"`
	Summary          *ViewSummary    `json:"summary,omitempty"`
	Page             []ViewReference `json:"page"`
	Next             string          `json:"next,omitempty"`
	CommittedThrough int64           `json:"committedThrough"`
	Provisional      bool            `json:"provisional"`
}
type viewSource struct {
	context       recordContext
	indexing      bool
	reader        populationReader
	api           *HTTP
	branch        Branch
	binding       viewBinding
	rows          []byte
	pop           *os.File
	release       func()
	memoryRelease func()
	ranges        []cursor
	assembler     frameAssembler
	snap          branchStore
	pending       []pending
	summaries     []Summary
	liveSummaries []Summary
	hasSummaries  bool
	store         *Store
}

func (s *viewSource) close() {
	if s.memoryRelease != nil {
		s.memoryRelease()
		s.memoryRelease = nil
	}
	if s.pop != nil {
		s.pop.Close()
	}
	if s.release != nil {
		s.release()
	}
}
func (s *Store) viewSnapshot(branch Branch, from, to time.Time, prior *viewBinding, summary *ViewSummary, payload bool) (*viewSource, error) {
	return s.captureView(branch, from, to, prior, summary, payload, true)
}

// A secondary-index read must never advance the ingestion finalization boundary.
func (s *Store) indexSnapshot(branch Branch) (*viewSource, error) {
	return s.captureView(branch, time.UnixMilli(0), time.UnixMilli(1<<48), nil, nil, true, false)
}
func (s *Store) captureView(branch Branch, from, to time.Time, prior *viewBinding, summary *ViewSummary, payload, finalize bool) (v *viewSource, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.check(); err != nil {
		return nil, err
	}
	b, err := s.openBranch(branch)
	if err != nil {
		return nil, err
	}
	if finalize {
		if err = s.flush(b); err != nil {
			return nil, err
		}
	}
	binding := viewBinding{Retired: b.baseRows, Definition: fmtDigestNative(s.layout.Digest), Generation: fmt.Sprintf("%x", b.generation), Branch: branch, Tail: b.tail(), Finalized: b.finalizedBefore}
	if prior != nil {
		if prior.Definition != binding.Definition || prior.Generation != binding.Generation || prior.Tail > binding.Tail || prior.Tail < b.baseRows || prior.Retired != b.baseRows {
			return nil, fmt.Errorf("source generation changed; restart search")
		}
		binding = *prior
	}
	var live string
	var events []pending
	var e error
	if finalize {
		live, events, e = b.captureLive(from, to, prior)
		if e != nil {
			return nil, e
		}
	}
	if !finalize {
		events = b.pendingAll()
	}
	binding.Live = live
	v = &viewSource{branch: branch, binding: binding, pending: events, store: s, indexing: !finalize}
	v.memoryRelease = b.pinMemory()
	cleanup := v
	defer func() {
		if err != nil {
			cleanup.close()
		}
	}()
	v.rows, v.release, err = b.snapshotIndex()
	if err != nil {
		return nil, err
	}
	v.rows = v.rows[:(binding.Tail-b.baseRows)*IndexRowSize]
	v.summaries = append([]Summary(nil), b.summaries...)
	v.hasSummaries = b.sum != nil
	if v.hasSummaries && prior == nil && finalize {
		for _, acc := range b.accumulators {
			var count uint64
			for _, n := range acc.columns.Count {
				count += n
			}
			if count == 0 {
				continue
			}
			sum, e := acc.snapshot(Run{Start: 0, End: count}, s.layout, &s.trie)
			if e != nil {
				return nil, e
			}
			v.liveSummaries = append(v.liveSummaries, sum)
		}
	}

	v.snap = branchStore{baseRows: b.baseRows, popRetired: b.popRetired, blockBase: b.blockBase, layout: s.layout, runs: append([]Run(nil), b.runs...), rows: v.rows, popEnd: b.popEnd, generation: b.generation}
	v.snap.time = b.time
	v.snap.timeAnchor = b.timeAnchor
	if !finalize && len(v.snap.runs) > 0 {
		first := v.snap.runs[0].PAC.Placement.From
		sealed := v.snap.runs[len(v.snap.runs)-1].PAC.Placement.To
		v.snap.time, err = buildHourDirectory(v.snap.runs, first, sealed, v.snap.tail(), v.snap.generation, b.hourOrigin(), [6]byte{4, 5, 5, 2, 2, 2})
		if err != nil {
			return nil, err
		}
	}
	if payload {
		v.pop, err = os.Open(b.pop.Name())
		if err != nil {
			return nil, err
		}
		v.snap.pop = v.pop
		v.assembler = frameAssembler{layout: s.layout, trie: &sharedTrie{nodes: append([]sharedNode(nil), s.trie.nodes...)}}
	}
	for _, c := range b.candidates(from, to, IndexRowSize) {
		if c.end > binding.Tail {
			c.end = binding.Tail
		}
		if c.at < c.end {
			v.ranges = append(v.ranges, c)
		}
	}
	if summary != nil {
		indexed := make([]uint64, len(summary.Counts))
		summed := make([]uint64, len(summary.Counts))
		// A run usually fits entirely in one chart bin. Visit only intersecting
		// bins rather than searching every run again for every chart column.
		for _, c := range v.ranges {
			first := int((v.snap.rowTime(c.at) - summary.From) / summary.Step)
			last := int((v.snap.rowTime(c.end-1) - summary.From) / summary.Step)
			first = max(0, first)
			last = min(len(indexed)-1, last)
			at := c.at
			for i := first; i <= last; i++ {
				hi := min(summary.To, summary.From+int64(i+1)*summary.Step)
				end := v.snap.lower(at, c.end, hi)
				indexed[i] += end - at
				at = end
			}
		}
		// Scan .sum once. Partial bins and groups beyond the captured tail fall
		// back to index counts; no .pop bytes or event objects are needed.
		if branch.Aggregates() && summary.From%300000 == 0 && summary.Step%300000 == 0 {
			for _, batch := range b.summaries {
				if batch.Through > binding.Tail {
					continue
				}
				for i := 0; i < batch.Columns.Len(); i++ {
					g := batch.Columns.At(i)
					if g.Period < summary.From || g.Period+300000 > summary.To {
						continue
					}
					i := int((g.Period - summary.From) / summary.Step)
					if i < len(summed) {
						summed[i] += g.Aggregate.Count
					}
				}
			}
		}
		for i, n := range indexed {
			if summed[i] == n {
				n = summed[i]
			}
			summary.Counts[i] += n
			summary.Count += n
			if branch == Error {
				summary.Errors[i] += n
			}
		}
	}
	if summary != nil {
		for _, p := range v.pending {
			i := (eventTime(p.id[:]) - summary.From) / summary.Step
			if i >= 0 && i < int64(len(summary.Counts)) {
				summary.Counts[i]++
				summary.Count++
				if branch == Error {
					summary.Errors[i]++
				}
			}
		}
	}
	if len(v.pending) > 0 {
		v.ranges = append(v.ranges, cursor{binding.Tail, binding.Tail + uint64(len(v.pending))})
	}
	return v, nil
}
