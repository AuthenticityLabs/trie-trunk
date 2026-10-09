package search

import (
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"trie-trunk/go/codec"
)

func TestPostingSelectionAcrossBufferedAndOverlappingRuns(t *testing.T) {
	c, _, _, now := fixture(t)
	c.BatchRecords = 2048
	d := testDefinition()
	d.Fields[4] = codec.Field{Name: "service", Enums: map[uint16]string{1: "vault", 2: "billing", 3: "orders", 4: "catalog"}}
	d.Path = []codec.Key{4, 3, 14}
	rev, data, err := d.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	m := mustOpen(t, c, rev, data)
	type event struct {
		id      string
		at      time.Time
		service uint64
		status  uint64
		uri     string
	}
	var events []event
	for run := 0; run < 3; run++ {
		for i := 0; i < 1024; i++ {
			e := event{at: now.Add(time.Duration(i*3+run) * time.Millisecond), service: uint64(i%4 + 1), status: uint64(200 + i%3), uri: fmt.Sprint(i % 7)}
			e.id = appendSearchRecord(t, m, rev, e.at, codec.Record{4: e.service, 14: e.status, 19: e.uri})
			events = append(events, e)
		}
		if err := m.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].id < events[j].id })
	start, end := now.Add(500*time.Millisecond), now.Add(2700*time.Millisecond)
	for _, test := range []struct {
		clauses []string
		accept  func(event) bool
	}{
		{nil, func(event) bool { return true }},
		{[]string{`service=="vault"`, `service=="billing"`, `service=="vault" http_status==201`}, func(e event) bool { return e.service <= 2 }},
		{[]string{`service=="vault" uri=="3"`, `service=="billing" http_status==201`}, func(e event) bool { return e.service == 1 && e.uri == "3" || e.service == 2 && e.status == 201 }},
		{[]string{`service=="absent"`}, func(event) bool { return false }},
	} {
		var expected []event
		for _, e := range events {
			if !e.at.Before(start) && e.at.Before(end) && test.accept(e) {
				expected = append(expected, e)
			}
		}
		for _, limit := range []uint64{0, 31, 1200} {
			for _, tail := range []bool{false, true} {
				q := Config{Root: c.Root, Start: start, End: end, Where: test.clauses, Tail: tail}
				want := expected
				if limit != 0 {
					q.Limit = &limit
					if uint64(len(want)) > limit {
						if tail {
							want = want[len(want)-int(limit):]
						} else {
							want = want[:limit]
						}
					}
				}
				rows := searchResults(t, q)
				var gotIDs, wantIDs []string
				for _, e := range want {
					wantIDs = append(wantIDs, e.id)
				}
				for i, row := range rows {
					gotIDs = append(gotIDs, row["id"].(string))
					if i < len(want) && (row["http_status"] != want[i].status || row["uri"] != want[i].uri || row["level"] != "ERROR" || row["duration"] != uint64(2234)) {
						t.Fatal("reconstruction changed", row, want[i])
					}
				}
				if !reflect.DeepEqual(gotIDs, wantIDs) {
					t.Fatalf("clauses=%v limit=%d tail=%t: got %d events, wanted %d", test.clauses, limit, tail, len(gotIDs), len(wantIDs))
				}
			}
		}
	}
}
