package cache

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"time"
)

type Span struct{ Start, End uint64 }

func Normalize(spans []Span) ([]Span, error) {
	out := append([]Span(nil), spans...)
	for _, r := range out {
		if r.End <= r.Start {
			return nil, fmt.Errorf("invalid coverage span")
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	n := 0
	for _, r := range out {
		if n > 0 && r.Start <= out[n-1].End {
			if r.End > out[n-1].End {
				out[n-1].End = r.End
			}
		} else {
			out[n] = r
			n++
		}
	}
	return out[:n], nil
}
func Missing(wanted, have []Span) ([]Span, error) {
	want, e := Normalize(wanted)
	if e != nil {
		return nil, e
	}
	have, e = Normalize(have)
	if e != nil {
		return nil, e
	}
	var out []Span
	j := 0
	for _, w := range want {
		at := w.Start
		for j < len(have) && have[j].End <= at {
			j++
		}
		for k := j; k < len(have) && have[k].Start < w.End; k++ {
			h := have[k]
			if h.Start > at {
				out = append(out, Span{at, h.Start})
			}
			if h.End > at {
				at = h.End
			}
			if at >= w.End {
				break
			}
		}
		if at < w.End {
			out = append(out, Span{at, w.End})
		}
	}
	return out, nil
}

type Coverage struct {
	Definition        [32]byte
	Generation        [16]byte
	Index, Population []Span
	TrieNodes         int
	StateKnown        bool
	StateEpoch        [16]byte
	StateSequence     uint64
}
type CollectionRequest struct {
	Branch                       Branch
	Day                          time.Time // Legacy explicit daily request; use At for policy-sized packages.
	At                           time.Time
	IndexPeriod                  string // Optional plan guard, e.g. 1h; reject a stale sizing plan.
	PopulationFrom, PopulationTo time.Time
	Have                         Coverage
	StatePages                   bool
}
type Manifest struct {
	TimeRetainedFrom                               int64
	TimeRetired                                    bool   `cbor:",omitempty"`
	TimeTrie                                       []byte `cbor:",omitempty"`
	RetiredRows, RetiredMembers, RetiredPopulation uint64
	Definition                                     [32]byte
	Generation                                     [16]byte
	GenerationSequence                             uint64
	Branch                                         Branch
	From, To                                       time.Time
	Tail                                           uint64
	Checkpoint                                     uint64
	RowWidth                                       int
	TrieNodes                                      int
	Index                                          []Span
	Population                                     []Span
	Provisional                                    bool
	IndexPeriod                                    string
	Timezone                                       string
	Blocks                                         []Run
	StateEpoch                                     [16]byte
	StateSequence                                  uint64
}
type NativeChunk struct {
	PAC    *StoredPAC
	Kind   string
	Start  uint64
	Data   []byte
	Digest [32]byte
}

// Collect publishes the source manifest first, then dependencies and missing
// native ranges. The caller must persist all chunks and the completion result
// before certifying coverage. A successful empty manifest certifies known-empty
// coverage at this checkpoint; an interrupted stream certifies nothing new.
// Collect supplies owned chunk bytes for callers that retain the result.
func (s *Store) Collect(ctx context.Context, request CollectionRequest, manifest func(Manifest) error, send func(NativeChunk) error) error {
	return s.collectBorrowed(ctx, request, manifest, func(chunk NativeChunk) error {
		chunk.Data = append([]byte(nil), chunk.Data...)
		return send(chunk)
	})
}

// collectBorrowed leases chunk bytes only for the duration of send. The HTTP
// writer consumes them synchronously, without creating an owned Go payload.
func (s *Store) collectBorrowed(ctx context.Context, request CollectionRequest, manifest func(Manifest) error, send func(NativeChunk) error) error {
	s.mu.Lock()
	locked := true
	defer func() {
		if locked {
			s.mu.Unlock()
		}
	}()
	if e := s.check(); e != nil {
		return e
	}
	if !request.Branch.Valid() {
		return fmt.Errorf("invalid branch")
	}
	if len(request.Have.Index) > 10000 || len(request.Have.Population) > 10000 {
		return fmt.Errorf("coverage advertisement too large")
	}
	if request.At.IsZero() == request.Day.IsZero() {
		return fmt.Errorf("provide either package At or legacy Day")
	}
	hours := s.policyFor(request.Branch).Replication.hours()
	at := request.At
	if at.IsZero() {
		at = request.Day
		hours = 24
	}
	period := fmt.Sprintf("%dh", hours)
	if request.IndexPeriod != "" && request.IndexPeriod != period {
		return fmt.Errorf("package policy changed; replan collection")
	}
	from, to := semanticBounds(at, hours, s.location)
	b, e := s.openBranch(request.Branch)
	if e != nil {
		return s.fail(e)
	}
	if e = s.flush(b); e != nil {
		return s.fail(e)
	}
	m := Manifest{Definition: s.layout.Digest, Generation: b.generation, GenerationSequence: s.generationSequence(request.Branch), Branch: request.Branch, From: from, To: to, Tail: b.tail(), Checkpoint: uint64(b.blockBase) + uint64(len(b.runs)), RowWidth: s.row.Width, TrieNodes: len(s.trie.nodes), IndexPeriod: period, Timezone: s.location.String(), Provisional: to.After(s.config.Now().Add(-s.provisionalTail(request.Branch))) || to.After(time.UnixMilli(b.finalizedBefore))}
	m.TimeRetainedFrom = b.time.first()
	m.RetiredRows = b.baseRows
	m.RetiredPopulation = b.popRetired
	m.RetiredMembers = b.memberEnd
	m.StateEpoch, m.StateSequence = s.state.epoch, s.state.sequence
	if !m.Provisional {
		start := from.UnixMilli()
		if b.baseRows > 0 && start < b.time.first() {
			m.TimeRetired = true
			start = b.time.first()
		}
		if start < to.UnixMilli() {
			m.TimeTrie, e = b.time.window(start, to.UnixMilli(), b.baseRows > 0)
			if e != nil {
				return e
			}
		}
	}
	b.visitPeriods(from, to, func(p *Period) {
		for _, r := range p.Ranges {
			m.Index = append(m.Index, Span{uint64(r.Start), r.end(m.Tail)})
		}
	})
	m.Index, e = Normalize(m.Index)
	if e != nil {
		return e
	}
	// Persistence is complete-hour only. Live/open searches use the in-memory
	// view path and cannot publish a partial disk coverage claim.
	if m.Provisional {
		m.Index = nil
	}
	for _, run := range b.runs {
		if !m.Provisional && run.PAC.Placement.From >= from.UnixMilli() && run.PAC.Placement.To <= to.UnixMilli() {
			m.Blocks = append(m.Blocks, run)
			p := run.PAC.Placement
			m.Population = append(m.Population, Span{p.MemberStart, p.MemberStart + p.MemberBytes})
		}
	}
	m.Population, e = Normalize(m.Population)
	if e != nil {
		return e
	}

	have := request.Have
	if have.Definition != m.Definition {
		have = Coverage{}
	} else if have.Generation != m.Generation {
		have.Index = nil
		have.Population = nil
	}
	var missingBlocks []Run
	var missingIndex []Span
	for _, run := range m.Blocks {
		p := run.PAC.Placement
		mi, err := Missing([]Span{{run.Start, run.End}}, have.Index)
		if err != nil {
			return err
		}
		mp, err := Missing([]Span{{p.MemberStart, p.MemberStart + p.MemberBytes}}, have.Population)
		if err != nil {
			return err
		}
		if len(mi)+len(mp) > 0 {
			missingBlocks = append(missingBlocks, run)
			missingIndex = append(missingIndex, Span{run.Start, run.End})
		}
	}
	if have.TrieNodes < 0 || have.TrieNodes > len(s.trie.nodes) {
		return fmt.Errorf("invalid cached trie prefix")
	}

	first := have.TrieNodes
	if first < 1 {
		first = 1
	}
	var trie []byte
	if first < len(s.trie.nodes) {
		trie, e = encoder.Marshal(s.trie.nodes[first:])
		if e != nil {
			return e
		}
	}
	state, e := s.snapshotState(have, request.StatePages)
	if e != nil {
		return e
	}
	// A generation's existing index bytes never change. Retain that committed
	// slice and an independent population descriptor before releasing the writer
	// lock. Retention can replace/unlink files while this snapshot stays readable.
	// Fully covered indexes (including population-only gap fills) have no row
	// bytes to send after unlocking. Avoid an otherwise unused mmap/munmap per
	// package; population boundaries were already captured under the mutex.
	var rows []byte
	if len(missingIndex) > 0 {
		var releaseIndex func()
		rows, releaseIndex, e = b.snapshotIndex()
		if e != nil {
			return e
		}
		defer releaseIndex()
	}
	var population *os.File
	if len(missingBlocks) > 0 {
		population, e = os.Open(b.pop.Name())
		if e != nil {
			return e
		}
		defer population.Close()
	}
	output := s.memory.buffer()
	defer output.close()
	baseRows, popRetired := b.baseRows, b.popRetired
	directory := b.time
	s.mu.Unlock()
	locked = false
	if e = ctx.Err(); e != nil {
		return e
	}
	if e = manifest(m); e != nil {
		return e
	}
	emit := func(kind string, start uint64, data []byte) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		return send(NativeChunk{Kind: kind, Start: start, Data: data, Digest: sha256.Sum256(data)})
	}
	if request.Have.Definition != m.Definition {
		if e = emit("map", 0, s.layout.Bytes); e != nil {
			return e
		}
	}
	if len(trie) > 0 {
		if e = emit("trie", uint64(first), trie); e != nil {
			return e
		}
	}
	if e = state.send(func(start uint64, raw []byte) error { return emit("state", start, raw) }); e != nil {
		return e
	}
	for _, run := range missingBlocks {
		if e = emit("index", run.Start, rows[(run.Start-baseRows)*IndexRowSize:(run.End-baseRows)*IndexRowSize]); e != nil {
			return e
		}
		leaf, ok := directory.leaf(run.PAC.Placement.From)
		if !ok {
			return fmt.Errorf("missing committed hour address")
		}
		start, end, err := storedSegmentRange(population, popRetired, leaf.Pods)
		if err != nil {
			return err
		}
		raw, err := output.readAt(population, start-popRetired, end-popRetired)
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = send(NativeChunk{Kind: "pac", Start: run.Start, Data: raw, Digest: sha256.Sum256(raw), PAC: run.PAC}); err != nil {
			return err
		}
	}
	return nil
}

// Extents use byte offsets (convert row numbers using the declared row width).
// They map packed local bytes to stable source addresses. A gap fill appends
// data and another extent; it never moves existing bytes or allocates fake holes.
type Extent struct {
	Source     Span
	LocalStart uint64
}

func Resolve(extents []Extent, wanted Span) ([]Extent, error) {
	if wanted.End <= wanted.Start {
		return nil, fmt.Errorf("invalid requested extent")
	}
	sorted := append([]Extent(nil), extents...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Source.Start < sorted[j].Source.Start })
	at := wanted.Start
	var out []Extent
	for i, e := range sorted {
		if e.Source.End <= e.Source.Start || i > 0 && e.Source.Start < sorted[i-1].Source.End {
			return nil, fmt.Errorf("invalid or overlapping local extents")
		}
		if e.Source.End <= at || e.Source.Start >= wanted.End {
			continue
		}
		if e.Source.Start > at {
			return nil, fmt.Errorf("source gap")
		}
		end := e.Source.End
		if end > wanted.End {
			end = wanted.End
		}
		out = append(out, Extent{Span{at, end}, e.LocalStart + (at - e.Source.Start)})
		at = end
	}
	if at != wanted.End {
		return nil, fmt.Errorf("source gap")
	}
	return out, nil
}
