package search

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"reflect"
	"sort"
	"testing"
	"time"
	"trie-trunk/go/codec"
)

func TestEventsNativePartitionsAndPIL(t *testing.T) {
	for _, format := range []uint32{3, 4} {
		t.Run(fmt.Sprint(format), func(t *testing.T) { testEventsNativePartitionsAndPIL(t, format) })
	}
}
func testEventsNativePartitionsAndPIL(t *testing.T, format uint32) {
	c, _, _, now := fixture(t)
	var want []string
	for _, zone := range []string{"America/New_York", "+05:30"} {
		if zone == "America/New_York" {
			c.Tap = "east"
		} else {
			c.Tap = "west"
		}
		d := testDefinition()
		d.Object = "logs"
		d.Version = 1 << 20
		d.Timezone = zone
		d.Storage = &codec.StoragePolicy{Format: format, Boundary: "resource", Bucket: "hour"}
		d.Fields[4] = codec.Field{Name: "resource", Enums: map[uint16]string{1: "secrets", 2: "jobs"}}
		d.Path = []codec.Key{3, 4, 14}
		rev, data, err := d.Prepare()
		if err != nil {
			t.Fatal(err)
		}
		m := mustOpen(t, c, rev, data)
		for _, mins := range []int{5, 130, 80, 1} {
			want = append(want, appendSearchRecord(t, m, rev, now.Add(-time.Duration(mins)*time.Minute), codec.Record{4: uint64(1 + mins%2)}))
			if err = m.Flush(); err != nil {
				t.Fatal(err)
			}
		}
		if err = m.Close(); err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(want)
	q := Config{Root: c.Root, Start: now.Add(-3 * time.Hour), End: now.Add(time.Second)}
	got := searchResults(t, q)
	var ids []string
	for _, v := range got {
		ids = append(ids, v["id"].(string))
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatal(ids, want)
	}
	q.Where = []string{`resource=="jobs"`}
	filtered := searchResults(t, q)
	if len(filtered) != 4 {
		t.Fatal("filtered", len(filtered))
	}
	var native bytes.Buffer
	if err := Run(context.Background(), q, &native); err != nil {
		t.Fatal(err)
	}
	q.PIL, q.PILV2 = true, true
	var wire bytes.Buffer
	if err := Run(context.Background(), q, &wire); err != nil {
		t.Fatal(err)
	}
	wire.WriteByte(0xfe) // HTTP collect adds the success marker after the PIL end.
	var decoded bytes.Buffer
	if err := DecodePIL(context.Background(), q, io.NopCloser(bytes.NewReader(wire.Bytes())), &decoded); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(native.Bytes(), decoded.Bytes()) {
		t.Fatal("PIL changed events")
	}
	limit := uint64(1)
	q.PIL, q.PILV2 = false, false
	q.Limit = &limit
	q.Tail = true
	if got := searchResults(t, q); len(got) != 1 || got[0]["id"] != filtered[len(filtered)-1]["id"] {
		t.Fatal(got)
	}
}
func TestEventsCompactPredecessorSurvivesSparseMatch(t *testing.T) {
	for _, format := range []uint32{3, 4} {
		t.Run(fmt.Sprint(format), func(t *testing.T) { testEventsCompactPredecessorSurvivesSparseMatch(t, format) })
	}
}
func testEventsCompactPredecessorSurvivesSparseMatch(t *testing.T, format uint32) {
	c, _, _, now := fixture(t)
	d := testDefinition()
	d.Storage = &codec.StoragePolicy{Format: format, Boundary: "missing", Bucket: "hour"}
	rev, data, err := d.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	m := mustOpen(t, c, rev, data)
	defer m.Close()
	for i := 0; i < 600; i++ {
		appendSearchRecord(t, m, rev, now.Add(-2*time.Hour+time.Duration(i)*time.Millisecond), codec.Record{14: uint64(400 + i%3), 19: "different population length " + string(rune('a'+i%20))})
	}
	if err = m.Flush(); err != nil {
		t.Fatal(err)
	}
	q := Config{Root: c.Root, Start: now.Add(-3 * time.Hour), End: *now, Where: []string{"http_status==401"}}
	if got := searchResults(t, q); len(got) != 200 {
		t.Fatal(len(got))
	}
}

func TestPartitionParallelOrder(t *testing.T) {
	for _, format := range []uint32{3, 4} {
		t.Run(fmt.Sprint(format), func(t *testing.T) { testPartitionParallelOrder(t, format) })
	}
}
func testPartitionParallelOrder(t *testing.T, format uint32) {
	c, _, _, now := fixture(t)
	d := testDefinition()
	d.Storage = &codec.StoragePolicy{Format: format, Boundary: "http_status", Bucket: "hour"}
	rev, data, e := d.Prepare()
	if e != nil {
		t.Fatal(e)
	}
	m := mustOpen(t, c, rev, data)
	defer m.Close()
	for i := 0; i < 80; i++ {
		appendSearchRecord(t, m, rev, now.Add(-2*time.Hour+time.Duration(i)*time.Millisecond), codec.Record{14: uint64(400 + i%5)})
	}
	if e = m.Flush(); e != nil {
		t.Fatal(e)
	}
	for _, tail := range []bool{false, true} {
		for _, where := range [][]string{nil, {"http_status!=402"}, {"uri==\"/secrets\""}} {
			n := uint64(17)
			q := Config{Root: c.Root, Start: now.Add(-3 * time.Hour), End: *now, Limit: &n, Tail: tail, Where: where}
			var want, got bytes.Buffer
			t.Setenv("TRUNK_SEARCH_WORKERS", "1")
			if e = Run(context.Background(), q, &want); e != nil {
				t.Fatal(e)
			}
			t.Setenv("TRUNK_SEARCH_WORKERS", "4")
			if e = Run(context.Background(), q, &got); e != nil {
				t.Fatal(e)
			}
			if !bytes.Equal(want.Bytes(), got.Bytes()) {
				t.Fatal("parallel order changed", tail, where)
			}
		}
	}
}

func TestFollowAcrossMaturityAndLateRun(t *testing.T) {
	for _, format := range []uint32{3, 4} {
		t.Run(fmt.Sprint(format), func(t *testing.T) { testFollowAcrossMaturityAndLateRun(t, format) })
	}
}
func testFollowAcrossMaturityAndLateRun(t *testing.T, format uint32) {
	c, _, _, now := fixture(t)
	*now = now.Truncate(time.Hour).Add(10 * time.Minute)
	d := testDefinition()
	d.Storage = &codec.StoragePolicy{Format: format, Boundary: "missing", Bucket: "hour"}
	rev, data, e := d.Prepare()
	if e != nil {
		t.Fatal(e)
	}
	m := mustOpen(t, c, rev, data)
	defer m.Close()
	first := appendSearchRecord(t, m, rev, now.Add(-20*time.Minute), nil)
	if e = m.Flush(); e != nil {
		t.Fatal(e)
	}
	rows, _ := followSearch(t, Config{Root: c.Root, Start: now.Add(-time.Hour), End: time.UnixMilli(1 << 48)})
	nextFollow(t, rows, first)
	*now = now.Add(5 * time.Minute)
	if e = m.Flush(); e != nil {
		t.Fatal(e)
	}
	late := appendSearchRecord(t, m, rev, now.Add(-40*time.Minute), nil)
	if e = m.Flush(); e != nil {
		t.Fatal(e)
	}
	nextFollow(t, rows, late)
	select {
	case extra := <-rows:
		t.Fatal("replayed prefix", extra)
	case <-time.After(300 * time.Millisecond):
	}
}
