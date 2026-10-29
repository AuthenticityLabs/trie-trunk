package cache

import (
	"fmt"
	"time"
)

// ViewBucket certifies the complete PIL event set, not native .idx/.pods bytes.
// Physical generation and definition are incorporated in Key. Row intervals
// come from the same semantic time trie used by native replication.
type ViewBucket struct {
	Key    string `json:"key"`
	Stream uint32 `json:"stream"`
	From   int64  `json:"from"`
	To     int64  `json:"to"`
	Ranges []Span `json:"ranges"`
	Sealed bool   `json:"sealed"`
}

func (v *viewSource) bucket(stream uint32, row uint64) (ViewBucket, error) {
	ms := v.snap.rowTime(row)
	s := v.api.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return ViewBucket{}, err
	}
	b := s.branches[v.branch]
	if b == nil || fmt.Sprintf("%x", b.generation) != v.binding.Generation {
		return ViewBucket{}, fmt.Errorf("source generation changed; restart search")
	}
	var out ViewBucket
	b.visitPeriods(time.UnixMilli(ms), time.UnixMilli(ms+1), func(p *Period) {
		out = ViewBucket{Stream: stream, From: p.From, To: p.To, Ranges: []Span{}, Sealed: p.To <= v.binding.Finalized && p.To <= s.config.Now().Add(-s.provisionalTail(v.branch)).UnixMilli()}
		for _, span := range p.Ranges {
			end := min(span.end(v.binding.Tail), v.binding.Tail)
			if uint64(span.Start) < end {
				out.Ranges = append(out.Ranges, Span{uint64(span.Start), end})
			}
		}
	})
	if len(out.Ranges) == 0 {
		return out, fmt.Errorf("missing event bucket")
	}
	var err error
	out.Ranges, err = Normalize(out.Ranges)
	if err != nil {
		return out, err
	}
	out.Key = viewDigest(struct {
		Definition, Generation string
		Branch                 Branch
		From, To               int64
		Ranges                 []Span
	}{v.binding.Definition, v.binding.Generation, v.branch, out.From, out.To, out.Ranges})
	return out, nil
}
