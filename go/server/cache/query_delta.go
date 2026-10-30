package cache

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Query receipts are advisory change detectors, not per-branch time checkpoints.
// The request has one common event-time checkpoint. No predicates run here.
var queryEpoch = func() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}()

type QueryReceipt struct {
	Tap, Definition, Generation string
	Sequence, Revision          uint64
	Sealed, Retained            int64
}
type queryDeltaRequest struct {
	From, To   int64
	Now        bool
	Checkpoint *int64
	Epoch      string
	PreviousTo int64
	Known      []QueryReceipt
	Targets    []QueryReceipt
}
type queryDeltaBranch struct {
	QueryReceipt `pil:"Receipt"`
	ReplaceFrom  int64
	TimeTrie     []byte
	Frames       [][]byte
}
type queryDeltaResponse struct {
	Epoch    string
	To       int64
	Branches []queryDeltaBranch
}

func (h *HTTP) queryDeltaHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "query delta requires POST", 405)
		return
	}
	if r.Header.Get("Content-Type") != "application/pil" {
		http.Error(w, "query delta requires PIL", 415)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	var q queryDeltaRequest
	if err != nil || decoder.Unmarshal(raw, &q) != nil || q.From < 0 || q.To <= q.From || q.To >= 1<<48 || q.To-q.From > 8*366*24*TimeHourMS || len(q.Known) > 4096 || len(q.Targets) > 4096 {
		http.Error(w, "invalid query delta", 400)
		return
	}
	// Capture now once; arriving records outside this bound belong to the next run.
	if q.Now {
		q.To = h.Store.config.Now().UnixMilli()
	}
	if q.To <= q.From {
		http.Error(w, "invalid query delta window", 400)
		return
	}
	if q.Checkpoint != nil && (*q.Checkpoint < 0 || *q.Checkpoint >= q.To) {
		http.Error(w, "invalid query checkpoint", 400)
		return
	}
	sources := h.sources
	if len(sources) == 0 {
		sources = []*HTTP{h}
	}
	// Targets are the same authorized catalog selection used by native retrieval.
	// The logical selector is separate from the predicate signature.
	selector := r.URL.Query().Get("tap")
	out := queryDeltaResponse{Epoch: queryEpoch, To: q.To, Branches: []queryDeltaBranch{}}
	total := 0
	targets := make(map[string]bool, len(q.Targets))
	for _, t := range q.Targets {
		targets[t.Tap+":"+t.Definition] = true
	}
	known := make(map[string]QueryReceipt, len(q.Known))
	for _, k := range q.Known {
		known[k.Tap+":"+k.Definition] = k
	}
	for _, api := range sources {
		branches, err := api.selectedBranches(selector)
		if err != nil {
			continue
		}
		for _, branch := range branches {
			if len(targets) > 0 && !targets[api.tap(branch)+":"+fmtDigestNative(api.Store.layout.Digest)] {
				continue
			}
			if err = r.Context().Err(); err != nil {
				return
			}
			part, err := api.queryDeltaBranch(branch, q, &total, known)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			out.Branches = append(out.Branches, part)
		}
	}
	if len(out.Branches) == 0 {
		http.Error(w, "unknown query scope", 404)
		return
	}
	raw, err = encoder.Marshal(out)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/pil")
	w.Header().Set("Trunk-Format", "query-delta/0")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}

func (h *HTTP) queryDeltaBranch(branch Branch, q queryDeltaRequest, total *int, receipts map[string]QueryReceipt) (queryDeltaBranch, error) {
	s := h.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return queryDeltaBranch{}, err
	}
	b, err := s.openBranch(branch)
	if err != nil {
		return queryDeltaBranch{}, err
	}
	b.queryObservedThrough = max(b.queryObservedThrough, q.To)
	// Do not trigger sealing or disk flushes on the refresh path. The sealer
	// publishes the time directory; open data already lives in the native arenas.
	// An empty directory can start at the current hour before the first seal.
	// Its header is not evidence that older staged events were published.
	sealed := min(b.time.sealed(), utcHour(b.live.firstPending()))
	part := queryDeltaBranch{QueryReceipt: QueryReceipt{Tap: h.tap(branch), Definition: fmtDigestNative(s.layout.Digest), Generation: fmt.Sprintf("%x", b.generation), Sequence: s.generationSequence(branch), Revision: b.pendingVersion, Sealed: sealed, Retained: min(b.time.first(), sealed)}, ReplaceFrom: q.From}
	var known *QueryReceipt
	if q.Epoch == queryEpoch {
		if k, ok := receipts[part.Tap+":"+part.Definition]; ok && k.Generation == part.Generation && k.Sequence == part.Sequence && k.Retained == part.Retained && k.Sealed <= part.Sealed && k.Revision <= part.Revision {
			known = &k
		}
	}
	start := utcHour(q.From)
	if known != nil {
		start = max(start, known.Sealed)
		if q.Checkpoint != nil {
			part.ReplaceFrom = max(q.From, *q.Checkpoint-1)
		}
		// Future-dated records may become visible without a new ingress revision.
		part.ReplaceFrom = min(part.ReplaceFrom, max(q.From, q.PreviousTo))
		if b.queryLateVersion > known.Revision {
			part.ReplaceFrom = min(part.ReplaceFrom, max(q.From, b.queryLateFrom))
		}
	}
	a, z := max(start, part.Retained), min(part.Sealed, utcHour(q.To-1)+TimeHourMS)
	if a < z {
		part.TimeTrie, err = b.time.window(a, z, true)
		if err != nil {
			return part, err
		}
	}
	part.ReplaceFrom = max(part.ReplaceFrom, part.Sealed)
	if known != nil && known.Revision == part.Revision {
		part.ReplaceFrom = max(part.Sealed, q.From, min(q.To, q.PreviousTo))
	}
	// Safe to borrow under the writer lock, copy each selected frame once before
	// releasing it. Do not build JSON event objects or evaluate event predicates.
	pending := b.pendingWindow(nil, time.UnixMilli(part.ReplaceFrom), time.UnixMilli(q.To))
	for _, p := range pending {
		*total += len(p.frame)
		if *total > 64<<20 {
			return part, fmt.Errorf("live query delta exceeds 64 MB; narrow the time window or scope")
		}
		part.Frames = append(part.Frames, append([]byte(nil), p.frame...))
	}
	return part, nil
}
