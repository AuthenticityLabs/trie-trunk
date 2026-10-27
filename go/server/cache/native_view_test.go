package cache

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestColdViewPlansWholePACWithoutPopulationReads(t *testing.T) {
	requireSearch(t)
	for _, branch := range []Branch{Error, Write} {
		t.Run(branch.Name(), func(t *testing.T) {
			l := testLayout(t, false)
			at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
			s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(72 * time.Hour) }})
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			off := false
			s.policy.SearchIndex = &off
			var frames [][]byte
			for i := uint32(1); i <= 10; i++ {
				frames = append(frames, testFrame(t, l, at.Add(time.Duration(i)*time.Minute), i, i%2 == 0))
			}
			if _, e = s.AppendBatch(branch, frames); e != nil {
				t.Fatal(e)
			}
			if e = s.Flush(); e != nil {
				t.Fatal(e)
			}
			on := true
			s.policy.SearchIndex = &on
			source, e := s.viewSnapshot(branch, at, at.Add(time.Hour), nil, nil, true)
			if e != nil {
				t.Fatal(e)
			}
			source.api = &HTTP{Store: s}
			q := QueryPlan{Filters: []QueryFilter{{"duration", ">", QueryLiteral{"duration", "11.5us"}}}, Order: "asc"}
			if e = source.filterQuery(context.Background(), &q); e != nil {
				t.Fatal(e)
			}
			source.close()
			// The disposable text index is ready. Corrupt population bytes deliberately:
			// planning, scalar search, whole-run statistics, and cursor generation may not read them.
			b := s.branches[branch]
			if _, e = b.pop.WriteAt([]byte("BROKEN!!"), int64(b.runs[0].PopStart-b.popRetired)); e != nil {
				t.Fatal(e)
			}
			h := &HTTP{Store: s}
			for _, query := range []*QueryPlan{nil, &q, {Filters: []QueryFilter{{"duration", "==", QueryLiteral{"null", ""}}}, Order: "asc"}, {Search: &QueryText{"REQUEST", "contains"}, Order: "asc"}} {
				params := url.Values{"tap": {h.tap(branch)}, "from": {at.Format(time.RFC3339)}, "to": {at.Add(time.Hour).Format(time.RFC3339)}, "page": {"2"}, "include": {"summary,page,statistics"}}
				if query != nil {
					raw, _ := json.Marshal(query)
					params.Set("query", string(raw))
				}
				r := httptest.NewRequest("GET", "/events?"+params.Encode(), nil)
				r.Header.Set("Accept", "application/pil")
				r.Header.Set("PIL-Version", "2")
				r.Header.Set("PIL-Features", "pac-arrays")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
				view := decodeView(t, l, w.Body.Bytes())
				if len(view.frames) != 0 {
					t.Fatal("sealed page was hydrated on server")
				}
				for _, ref := range view.result.Page {
					if ref.Native == nil || ref.Native.From != at.UnixMilli() {
						t.Fatal("missing canonical hour")
					}
				}
				if query == nil && (!view.result.Summary.Statistics.Complete || view.result.Summary.Statistics.Duration.Count != 5) {
					t.Fatal("whole-range accumulator missing")
				}
			}
		})
	}
}
