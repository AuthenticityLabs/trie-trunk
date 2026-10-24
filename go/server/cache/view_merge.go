package cache

// Merge ordered per-source candidates without hydrating event payloads.
import ()

type viewHit struct {
	source   int
	row, end uint64
}
type viewHeap struct {
	desc    bool
	hits    []viewHit
	sources []*viewSource
}

func (h viewHeap) Len() int { return len(h.hits) }
func (h viewHeap) Less(i, j int) bool {
	n := compareEvent(h.sources, h.hits[i], h.hits[j])
	if h.desc {
		return n > 0
	}
	return n < 0
}

func (h viewHeap) Swap(i, j int) { h.hits[i], h.hits[j] = h.hits[j], h.hits[i] }
func (h *viewHeap) Push(v any)   { h.hits = append(h.hits, v.(viewHit)) }
func (h *viewHeap) Pop() any     { n := len(h.hits) - 1; v := h.hits[n]; h.hits = h.hits[:n]; return v }
