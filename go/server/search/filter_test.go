package search

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"trie-trunk/go/codec"
)

func TestWhereAcrossLineagesPayloadAndVersions(t *testing.T) {
	for _, maxNodes := range []int{1, 65536} {
		t.Run(fmt.Sprint(maxNodes), func(t *testing.T) {
			c, _, _, now := fixture(t)
			c.MaxNodes = maxNodes
			d := testDefinition()
			d.Fields[4] = codec.Field{Name: "service", Enums: map[uint16]string{1: "vault", 2: "billing"}}
			d.Fields[15] = codec.Field{Name: "message"}
			d.Fields[22] = codec.Field{Name: "active"}
			d.Fields[23] = codec.Field{Name: "amount"}
			d.Path = []codec.Key{4, 3, 14, 2}
			rev, data, err := d.Prepare()
			if err != nil {
				t.Fatal(err)
			}
			m := mustOpen(t, c, rev, data)
			records := []codec.Record{
				{4: uint64(1), 15: "access denied", 22: false, 23: uint64(0)},
				{4: uint64(1), 14: uint64(200), 15: "ok"},
				{4: uint64(2), 16: codec.Record{0: uint64(1)}},
				{4: uint64(2)},
				{4: "vault", 15: "access denied", 16: codec.Record{}},
				{15: "", 23: ^uint64(0)},
			}
			var ids []string
			for i, record := range records {
				ids = append(ids, appendSearchRecord(t, m, rev, now.Add(time.Duration(i)*time.Millisecond), record))
			}
			if err := m.Flush(); err != nil {
				t.Fatal(err)
			}
			d.Version++
			delete(d.Fields, 4)
			d.Fields[5] = codec.Field{Name: "service", Enums: map[uint16]string{1: "billing", 2: "vault"}}
			d.Path[0] = 5
			next, definitions, err := d.Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if err := m.Install(next, definitions); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, appendSearchRecord(t, m, next, now.Add(6*time.Millisecond), codec.Record{5: uint64(2)}))
			if err := m.Flush(); err != nil {
				t.Fatal(err)
			}
			q := Config{Root: c.Root, Start: *now, End: now.Add(time.Second)}
			for _, test := range []struct {
				clauses []string
				want    []int
			}{
				{[]string{`service=="vault" http_status==401`, `service=="billing" error is !null`}, []int{0, 2, 4, 6}},
				{[]string{`service!="vault"`}, []int{2, 3}},
				{[]string{`service is null`}, []int{5}},
				{[]string{`service is !null`}, []int{0, 1, 2, 3, 4, 6}},
				{[]string{`error is !null`}, []int{2, 4}},
				{[]string{`error is null`}, []int{0, 1, 3, 5, 6}},
				{[]string{`message=="access denied"`}, []int{0, 4}},
				{[]string{`message!="ok"`}, []int{0, 4, 5}},
				{[]string{`active==false amount==0 active is !null`}, []int{0}},
				{[]string{`amount==18446744073709551615`}, []int{5}},
				{[]string{`http_status=="401"`}, nil},
				{[]string{`service==1`}, nil},
				{[]string{`missing!=1`}, nil},
				{[]string{`missing is null`}, []int{0, 1, 2, 3, 4, 5, 6}},
				{[]string{`service=="vault"`, `service is !null`}, []int{0, 1, 2, 3, 4, 6}},
				{[]string{`service=="vault" service=="billing"`}, nil},
				{[]string{`id=="` + ids[3] + `"`}, []int{3}},
			} {
				q.Where = test.clauses
				var want, got []string
				for _, i := range test.want {
					want = append(want, ids[i])
				}
				for _, row := range searchResults(t, q) {
					got = append(got, row["id"].(string))
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatal(test.clauses, got, want)
				}
			}
			q.Where = []string{`message=="access denied"`}
			count := uint64(1)
			q.Limit = &count
			for _, tail := range []bool{false, true} {
				q.Tail = tail
				want := ids[0]
				if tail {
					want = ids[4]
				}
				rows := searchResults(t, q)
				if len(rows) != 1 || rows[0]["id"] != want {
					t.Fatal(tail, rows, want)
				}
			}
		})
	}
}

func TestWhereFollowRejectsThenAcceptsAndRebinds(t *testing.T) {
	c, rev, data, now := fixture(t)
	m := mustOpen(t, c, rev, data)
	first := appendSearchRecord(t, m, rev, *now, codec.Record{19: "wanted"})
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	count := uint64(1)
	rows, _ := followSearch(t, Config{Root: c.Root, Start: now.Add(-time.Hour), End: time.UnixMilli(1 << 48), Limit: &count, Tail: true, Where: []string{`level=="ERROR" uri=="wanted"`, `level=="INFO" uri=="new"`}})
	nextFollow(t, rows, first)
	appendSearchRecord(t, m, rev, now.Add(time.Millisecond), codec.Record{19: "rejected"})
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	select {
	case row := <-rows:
		t.Fatal("rejected row emitted", row)
	case <-time.After(350 * time.Millisecond):
	}
	late := appendSearchRecord(t, m, rev, now.Add(-time.Minute), codec.Record{19: "wanted"})
	newPath := appendSearchRecord(t, m, rev, now.Add(2*time.Millisecond), codec.Record{3: uint64(3), 19: "new"})
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	nextFollow(t, rows, late)
	nextFollow(t, rows, newPath)
	d := testDefinition()
	d.Version = rev.Version + 1
	d.Fields[3] = codec.Field{Name: "level", Enums: map[uint16]string{3: "ERROR", 5: "INFO"}}
	next, definitions, err := d.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Install(next, definitions); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(24 * time.Hour)
	changed := appendSearchRecord(t, m, next, *now, codec.Record{19: "new"})
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	nextFollow(t, rows, changed)
	select {
	case row := <-rows:
		t.Fatal("replayed row", row)
	case <-time.After(350 * time.Millisecond):
	}
}

func TestWhereDoesNotReadExcludedPayload(t *testing.T) {
	c, rev, data, now := fixture(t)
	m := mustOpen(t, c, rev, data)
	appendSearchRecord(t, m, rev, *now, nil)
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.Root, "0", fmt.Sprint(rev.Version), now.Format("2006/002"), "log.pods")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	q := Config{Root: c.Root, Start: *now, End: now.Add(time.Second), Where: []string{`level=="INFO"`}}
	if rows := searchResults(t, q); len(rows) != 0 {
		t.Fatal(rows)
	}
	q.Where = []string{`level=="ERROR"`}
	var output bytes.Buffer
	if err := Run(context.Background(), q, &output); err == nil || !strings.Contains(err.Error(), "payload checksum") {
		t.Fatal(err)
	}
}

func TestWhereExplicitNull(t *testing.T) {
	c, rev, data, now := fixture(t)
	c.BatchRecords = 1
	m := mustOpen(t, c, rev, data)
	id := appendSearchRecord(t, m, rev, *now, codec.Record{16: codec.Record{}})
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	// The older log-ingress profile rejects null fields. Construct a native
	// PIL fixture at rest to exercise the search evaluator's null semantics.
	dir := filepath.Join(c.Root, "0", fmt.Sprint(rev.Version), now.Format("2006/002"))
	pods, err := os.ReadFile(filepath.Join(dir, "log.pods"))
	if err != nil {
		t.Fatal(err)
	}
	fields, err := codec.Decode(pods[16:], codec.PIL)
	if err != nil {
		t.Fatal(err)
	}
	fields[16] = nil
	payload, err := codec.MarshalCompact(fields, codec.PIL)
	if err != nil {
		t.Fatal(err)
	}
	block := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(block, uint32(len(payload)))
	binary.BigEndian.PutUint32(block[4:], crc32.Checksum(payload, crc32.MakeTable(crc32.Castagnoli)))
	copy(block[8:], payload)
	pods = append(pods[:8:8], block...)
	if err = os.WriteFile(filepath.Join(dir, "log.pods"), pods, 0600); err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(dir, "log.idx"))
	if err != nil {
		t.Fatal(err)
	}
	if len(index) != 56 || binary.BigEndian.Uint32(index[16:20]) != 1 {
		t.Fatal("expected one native index record")
	}
	binary.BigEndian.PutUint64(index[len(index)-8:], uint64(len(pods)))
	binary.BigEndian.PutUint32(index[12:16], crc32.Checksum(index[16:], crc32.MakeTable(crc32.Castagnoli)))
	if err = os.WriteFile(filepath.Join(dir, "log.idx"), index, 0600); err != nil {
		t.Fatal(err)
	}
	q := Config{Root: c.Root, Start: *now, End: now.Add(time.Second), Where: []string{`error is null`}}
	rows := searchResults(t, q)
	if len(rows) != 1 || rows[0]["id"] != id || rows[0]["error"] != nil {
		t.Fatal(rows)
	}
	q.Where = []string{`error is !null`}
	if rows := searchResults(t, q); len(rows) != 0 {
		t.Fatal(rows)
	}
}

func TestWhereFailsBeforeOpeningStore(t *testing.T) {
	for _, clause := range []string{"", `x==1 AND y==2`, `x==1 OR y==2`, `(x==1)`, `x==null`} {
		q := Config{Root: filepath.Join(t.TempDir(), "absent"), Start: time.UnixMilli(0), End: time.UnixMilli(1), Where: []string{clause}}
		var output bytes.Buffer
		if err := Run(context.Background(), q, &output); err == nil || strings.Contains(err.Error(), "No such file") || output.Len() != 0 {
			t.Fatal(clause, err)
		}
	}
}
