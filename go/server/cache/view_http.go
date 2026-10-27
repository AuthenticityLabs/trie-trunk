package cache

// HTTP view orchestration and PAC/control framing; storage is owned by pinned snapshots.
import (
	"bufio"
	"bytes"
	"container/heap"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const viewMaxBytes = 16 << 20

var viewSecret = func() []byte {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return b
}()

func (h *HTTP) viewHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET required", 405)
		return
	}
	if r.Header.Get("PIL-Version") != "2" || !strings.Contains(","+r.Header.Get("PIL-Features")+",", ",pac-arrays,") {
		http.Error(w, "PIL/2 pac-arrays capability required", 426)
		return
	}
	if !strings.EqualFold(r.Header.Get("Accept"), "application/pil") {
		http.Error(w, "application/pil required", 406)
		return
	}
	q := r.URL.Query()
	for k, v := range q {
		if len(v) != 1 || !map[string]bool{"tap": true, "from": true, "to": true, "page": true, "after": true, "include": true, "query": true}[k] {
			http.Error(w, "unsupported or duplicate view parameter", 400)
			return
		}
	}
	plan, planKey, err := parseQueryPlan(q.Get("query"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	filtered := plan != nil && (len(plan.Filters) > 0 || plan.Search != nil)
	resolution := Resolution(r.Header.Get("Summary-Resolution"))
	if resolution != "" && (filtered || resolution != Weekly && resolution != Monthly && resolution != Yearly || !strings.Contains(","+r.Header.Get("PIL-Features")+",", ",daily-summaries,")) {
		http.Error(w, "Summary-Resolution requires unfiltered daily summaries and week, month or year", 400)
		return
	}
	from, e := time.Parse(time.RFC3339Nano, q.Get("from"))
	if e != nil {
		http.Error(w, "invalid from", 400)
		return
	}
	to, e := time.Parse(time.RFC3339Nano, q.Get("to"))
	if e != nil || !from.Before(to) {
		http.Error(w, "invalid time window", 400)
		return
	}
	size := 50
	if q.Has("page") {
		size, e = strconv.Atoi(q.Get("page"))
		if e != nil || size < 1 || size > 1000 {
			http.Error(w, "page must be 1..1000", 400)
			return
		}
	}
	include := q.Get("include")
	if include == "" {
		include = "summary,page"
	}
	wantSummary, wantPage, wantStatistics := false, false, false
	for _, part := range strings.Split(include, ",") {
		switch part {
		case "summary":
			wantSummary = true
		case "statistics":
			wantSummary = true
			wantStatistics = true
		case "page":
			wantPage = true
		default:
			http.Error(w, "include must be summary, statistics and/or page", 400)
			return
		}
	}
	auth := r.Header.Get("Authorization")
	c := viewCursor{Query: planKey, Scope: q.Get("tap"), From: from.Format(time.RFC3339Nano), To: to.Format(time.RFC3339Nano), Expires: time.Now().Add(15 * time.Minute).Unix()}
	continuing := q.Get("after") != ""
	if continuing {
		c, e = viewParse(q.Get("after"), auth)
		if e != nil {
			http.Error(w, e.Error(), 409)
			return
		}
		if c.Query != planKey || c.Scope != q.Get("tap") || c.From != from.Format(time.RFC3339Nano) || c.To != to.Format(time.RFC3339Nano) {
			http.Error(w, "cursor selection changed", 409)
			return
		}
	}
	wantBuckets := !filtered && strings.Contains(","+r.Header.Get("PIL-Features")+",", ",view-buckets,")
	knownBuckets := map[string]bool{}
	if raw := r.Header.Get("Bucket-Known"); raw != "" {
		if !wantBuckets || len(raw) > 8192 {
			http.Error(w, "invalid bucket coverage", 431)
			return
		}
		for _, key := range strings.Split(raw, ",") {
			if len(key) != 64 {
				http.Error(w, "invalid bucket key", 400)
				return
			}
			if _, err := hex.DecodeString(key); err != nil {
				http.Error(w, "invalid bucket key", 400)
				return
			}
			knownBuckets[key] = true
		}
	}
	knownEvents := map[string]bool{}
	raw := r.Header.Get("Event-Known")
	if len(raw) > 16384 {
		http.Error(w, "event coverage exceeds 16 KiB", 431)
		return
	}
	for _, key := range strings.Split(raw, ",") {
		if key != "" {
			knownEvents[key] = true
		}
	}
	apis := h.sources
	if len(apis) == 0 {
		apis = []*HTTP{h}
	}
	type target struct {
		api    *HTTP
		branch Branch
		prior  *viewBinding
	}
	var targets []target
	if continuing {
		for i := range c.Sources {
			b := &c.Sources[i]
			var found *HTTP
			for _, api := range apis {
				if fmtDigestNative(api.Store.layout.Digest) == b.Definition {
					branches, _ := api.selectedBranches(c.Scope)
					for _, branch := range branches {
						if branch == b.Branch {
							found = api
						}
					}
				}
			}
			if found == nil {
				http.Error(w, "snapshot source unavailable; restart search", 409)
				return
			}
			targets = append(targets, target{found, b.Branch, b})
		}
	} else {
		for _, api := range apis {
			branches, _ := api.selectedBranches(c.Scope)
			for _, b := range branches {
				targets = append(targets, target{api, b, nil})
			}
		}
	}
	if len(targets) == 0 {
		http.Error(w, "unknown source", 404)
		return
	}
	if len(targets) > 128 {
		http.Error(w, "view spans too many sources; narrow service/resource", 400)
		return
	}
	for _, t := range targets {
		if t.api.Store.layout.Adaptive() && !hasAdaptive(r) {
			http.Error(w, "adaptive-integers capability required", 426)
			return
		}
	}
	// First capture tails; the summary version is computable without reading any
	// payload. Reused summary versions bypass aggregation entirely.
	var sources []*viewSource
	defer func() {
		for _, s := range sources {
			s.close()
		}
	}()
	for _, t := range targets {
		if e = r.Context().Err(); e != nil {
			return
		}
		s, err := t.api.Store.viewSnapshot(t.branch, from, to, t.prior, nil, wantPage || wantStatistics || filtered)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		s.api = t.api
		sources = append(sources, s)
		if !continuing {
			c.Sources = append(c.Sources, s.binding)
		}
	}
	if e = validateQueryPlan(plan, sources); e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	for _, s := range sources {
		if e = s.filterQuery(r.Context(), plan); e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
	}
	snapshot := viewDigest(struct {
		Query           string
		Scope, From, To string
		Sources         []viewBinding
		Statistics      bool
	}{c.Query, c.Scope, c.From, c.To, c.Sources, wantStatistics})
	summaryVersion := snapshot
	if resolution != "" {
		summaryVersion = viewDigest(struct {
			Snapshot   string
			Resolution Resolution
		}{snapshot, resolution})
	}
	result := ViewResult{Snapshot: snapshot, SummaryVersion: summaryVersion, Page: []ViewReference{}, CommittedThrough: to.UnixMilli()}
	for _, s := range sources {
		if s.binding.Finalized < result.CommittedThrough {
			result.CommittedThrough = s.binding.Finalized
		}
	}
	result.Provisional = to.UnixMilli() > result.CommittedThrough
	if wantSummary && r.Header.Get("Summary-Known") != summaryVersion {
		step := (timeBound(to) - timeBound(from) + 63) / 64
		step = ((step + 299999) / 300000) * 300000
		if step < 300000 {
			step = 300000
		}
		daily := !continuing && !filtered && strings.Contains(","+r.Header.Get("PIL-Features")+",", ",daily-summaries,")
		summary := &ViewSummary{From: timeBound(from), To: timeBound(to), Step: step, Status: "committed-snapshot"}
		if daily {
			summary.From = summary.From / sumBucketMS * sumBucketMS
		}
		for _, s := range sources {
			if s.binding.Live != "" {
				summary.Status = "live-snapshot"
			}
		}
		n := (summary.To - summary.From + step - 1) / step
		summary.Counts = make([]uint64, n)
		summary.Errors = make([]uint64, n)
		// Reopen at the captured tails; a retention replacement is an explicit retry,
		// never a summary computed against a different population than the page.
		statSources := sources
		if daily {
			result.Analytics, statSources, e = buildViewAnalytics(sources, timeBound(from), timeBound(to), resolution)
			if e != nil {
				http.Error(w, e.Error(), 409)
				return
			}
		}
		if result.Analytics != nil {
			addViewCounts(summary, statSources)
		} else if filtered {
			filteredSummary(summary, sources)
		} else {
			for _, s := range sources {
				a, err := s.api.Store.viewSnapshot(s.branch, from, to, &s.binding, summary, false)
				if err != nil {
					http.Error(w, err.Error(), 409)
					return
				}
				a.close()
			}
		}
		if wantStatistics {
			if result.Analytics != nil {
				var border summaryBorder
				summary.Statistics, e = viewStatistics(r.Context(), statSources, border.add)
				result.Analytics.Border = border.rows()
			} else {
				summary.Statistics, e = viewStatistics(r.Context(), statSources)
			}
			if e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
		}

		result.Summary = summary
	}
	result.Cursor, e = viewSign(c, auth)
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	queue := viewHeap{sources: sources, desc: plan != nil && plan.Order == "desc"}
	last, _ := hex.DecodeString(c.LastID)
	for i, s := range sources {
		for _, span := range s.ranges {
			start, end := span.at, span.end
			if len(last) > 0 {
				boundary := uint64(sort.Search(int(end-start), func(n int) bool {
					row := span.at + uint64(n)
					cmp := bytes.Compare(s.eventID(row), last)
					if queue.desc {
						return cmp > 0 || cmp == 0 && (i > c.LastSource || i == c.LastSource && row >= c.LastRow)
					}
					return cmp > 0 || cmp == 0 && (i > c.LastSource || i == c.LastSource && row > c.LastRow)
				}))
				if queue.desc {
					end = start + boundary
				} else {
					start += boundary
				}
			}
			if start < end {
				if queue.desc {
					queue.hits = append(queue.hits, viewHit{i, end - 1, start})
				} else {
					queue.hits = append(queue.hits, viewHit{i, start, end})
				}
			}
		}
	}
	heap.Init(&queue)
	type record struct {
		stream uint32
		raw    []byte
	}
	var records []record
	total := 0
	bucketBySource := map[int][]ViewBucket{}
	if wantPage {
		for queue.Len() > 0 && len(result.Page) < size {
			if e = r.Context().Err(); e != nil {
				return
			}
			hit := queue.hits[0]
			s := sources[hit.source]
			row := s.eventID(hit.row)
			provisional := hit.row >= s.binding.Tail
			key := viewDigest(struct {
				Binding viewBinding
				Row     uint64
			}{viewBinding{Definition: s.binding.Definition, Generation: s.binding.Generation, Branch: s.branch}, hit.row})
			var start, end uint64
			var err error
			if provisional {
				end = uint64(len(s.pending[hit.row-s.binding.Tail].frame))
				key = viewDigest(struct {
					Definition string
					Generation string
					Branch     Branch
					ID         string
				}{s.binding.Definition, s.binding.Generation, s.branch, hex.EncodeToString(row)})
			} else {
				end = uint64(s.snap.memberLength(hit.row))
			}
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			// Cap even cached pages; first oversize frame is allowed up to codec MaxFrame.
			if total+int(end-start)+4096 > viewMaxBytes && len(result.Page) > 0 {
				break
			}
			bucketKnown := false
			if wantBuckets && !provisional {
				ms := eventTime(row)
				var bucket *ViewBucket
				for i := range bucketBySource[hit.source] {
					b := &bucketBySource[hit.source][i]
					if ms >= b.From && ms < b.To {
						bucket = b
						break
					}
				}
				if bucket == nil {
					b, err := s.bucket(uint32(hit.source), hit.row)
					if err != nil {
						http.Error(w, err.Error(), 409)
						return
					}
					bucketBySource[hit.source] = append(bucketBySource[hit.source], b)
					result.Buckets = append(result.Buckets, b)
					bucket = &b
				}
				bucketKnown = bucket.Sealed && knownBuckets[bucket.Key]
			}
			ref := ViewReference{Stream: uint32(hit.source), Row: hit.row, Key: key, Cached: knownEvents[key] || bucketKnown, Provisional: provisional}
			if !provisional {
				run := s.snap.runs[s.snap.runIndex(hit.row)]
				ref.Native = &viewNativeReference{run.PAC.Placement.From, run.PAC.Placement.To, s.binding.Generation}
			}
			result.Page = append(result.Page, ref)
			if !ref.Cached && provisional {
				raw, err := s.fullFrame(hit.row)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				records = append(records, record{ref.Stream, raw})
			}
			total += int(end-start) + 4096
			c.LastID = hex.EncodeToString(row[:16])
			c.LastSource = hit.source
			c.LastRow = hit.row
			finished := queue.desc && hit.row == hit.end || !queue.desc && hit.row+1 == hit.end
			if !finished {
				if queue.desc {
					queue.hits[0].row--
				} else {
					queue.hits[0].row++
				}
			}
			if finished {
				heap.Pop(&queue)
			} else {
				heap.Fix(&queue, 0)
			}
		}
		if queue.Len() > 0 {
			result.Next, e = viewSign(c, auth)
			if e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
		}
	}
	w.Header().Set("Content-Type", "application/pil")
	w.Header().Set("PIL-Version", "2")
	definitionIDs := make([]string, 0, len(sources))
	definitionSeen := map[string]bool{}
	for _, source := range sources {
		if !definitionSeen[source.binding.Definition] {
			definitionIDs = append(definitionIDs, source.binding.Definition)
			definitionSeen[source.binding.Definition] = true
		}
	}
	sort.Strings(definitionIDs)
	if len(definitionIDs) == 1 {
		w.Header().Set("Definition-Version", definitionIDs[0])
	} else {
		w.Header().Set("Definition-Version", viewDigest(definitionIDs))
	}
	w.Header().Set("Search-Snapshot", snapshot)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Trunk-Format", "pac-view/0")
	w.Header().Set("Trunk-Completion", "body")
	w.Header().Set("Vary", "Accept, Authorization, PIL-Version, PIL-Features, Definition-Known, Definition-Version, Summary-Known, Summary-Resolution, Event-Known, Bucket-Known")
	var out interface {
		io.Writer
		Flush() error
	}
	if acceptsGzip(r) {
		w.Header().Set("Content-Encoding", "gzip")
		gz := NewGzipPACWriter(w)
		defer gz.Close()
		out = gz
	} else {
		out = bufio.NewWriterSize(w, 128<<10)
	}
	w.Header().Add("Vary", "Accept-Encoding")
	out.Write([]byte{'T', 'P', 'I', 'L', 2})
	known := "," + r.Header.Get("Definition-Known") + ","
	defined := map[string]bool{}
	for i, s := range sources {
		l := s.api.Store.layout
		d := fmtDigestNative(l.Digest)
		if !defined[d] && !strings.Contains(known, ","+d+",") && r.Header.Get("Definition-Version") != d {
			if e = writeDefine(out, l); e != nil {
				return
			}
		}
		defined[d] = true
		if e = writeBind(out, uint32(i), l.Digest, s.api.tap(s.branch)); e != nil {
			return
		}
	}
	meta, _ := json.Marshal(result)
	if e = writeViewControl(out, meta); e != nil {
		return
	}
	for i := 0; i < len(records); {
		j := i + 1
		for j < len(records) && records[j].stream == records[i].stream {
			j++
		}
		l := sources[records[i].stream].api.Store.layout
		frames := make([][]byte, 0, j-i)
		flush := func() error {
			if len(frames) == 0 {
				return nil
			}
			err := writePAC(out, l, sources[records[i].stream].api.tap(sources[records[i].stream].branch), frames)
			frames = frames[:0]
			return err
		}
		for n := i; n < j; n++ {
			raw := records[n].raw
			raw, e = l.ExpandEvent(raw)
			if e != nil {
				return
			}
			frames = append(frames, raw)
		}
		if e = flush(); e != nil {
			return
		}
		i = j
	}
	out.Write([]byte{255, 254})
	out.Flush()
}

// Tag 7 carries a complete version-zero PAC with a stable definition binding.
func writePAC(w io.Writer, l *Layout, source string, frames [][]byte) error {
	p, err := PackPAC(l, frames, []PACExtension{{Type: PACSource, Data: []byte(source)}}, true)
	if err != nil {
		return err
	}
	if packed, ok := w.(interface{ PAC(*PAC) error }); ok {
		return packed.PAC(p)
	}
	if _, err = w.Write([]byte{7}); err != nil {
		return err
	}
	_, err = w.Write(p.Bytes)
	return err
}
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		if !strings.EqualFold(fields[0], "gzip") {
			continue
		}
		quality := 1.0
		for _, f := range fields[1:] {
			k, v, ok := strings.Cut(strings.TrimSpace(f), "=")
			if ok && strings.EqualFold(k, "q") {
				q, e := strconv.ParseFloat(v, 64)
				if e != nil || q < 0 || q > 1 {
					return false
				}
				quality = q
			}
		}
		return quality > 0
	}
	return false
}
func writeViewControl(w io.Writer, raw []byte) error {
	return writeControl(w, 8, viewControl, bytesValue(raw))
}
