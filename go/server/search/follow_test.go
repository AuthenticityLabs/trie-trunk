package search

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"trie-trunk/go/codec"
)

func TestSearchHeadTailAcrossDays(t *testing.T) {
	c, rev, data, now := fixture(t)
	m := mustOpen(t, c, rev, data)
	var ids []string
	for _, offset := range []time.Duration{-25 * time.Hour, -time.Minute, -24 * time.Hour, -2 * time.Minute, 0} {
		ids = append(ids, appendSearchRecord(t, m, rev, now.Add(offset), nil))
		if err := m.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(ids)
	for _, tail := range []bool{false, true} {
		for _, count := range []uint64{0, 1, 3, 5, 10} {
			q := Config{Root: c.Root, Start: time.UnixMilli(0), End: time.UnixMilli(1 << 48), Limit: &count, Tail: tail}
			rows := searchResults(t, q)
			n := min(int(count), len(ids))
			if len(rows) != n {
				t.Fatal(tail, count, rows)
			}
			begin := 0
			if tail {
				begin = len(ids) - n
			}
			for i, row := range rows {
				if row["id"] != ids[begin+i] {
					t.Fatal(tail, count, rows, ids)
				}
			}
		}
	}
}

func followSearch(t *testing.T, config Config) (<-chan map[string]any, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	rows := make(chan map[string]any, 16)
	done := make(chan error, 1)
	config.Follow = true
	go func() {
		done <- readSearch(ctx, config, func(row map[string]any) error {
			select {
			case rows <- row:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	stop := func() {
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("follow did not stop")
		}
	}
	t.Cleanup(stop)
	return rows, cancel
}

func nextFollow(t *testing.T, rows <-chan map[string]any, id string) map[string]any {
	t.Helper()
	select {
	case row := <-rows:
		if row["id"] != id {
			t.Fatalf("wanted %s, got %v", id, row)
		}
		return row
	case <-time.After(5 * time.Second):
		t.Fatal("follow did not emit", id)
		return nil
	}
}

func TestFollowLateEventsTrieGrowthAndNewPartitions(t *testing.T) {
	c, rev, data, now := fixture(t)
	m := mustOpen(t, c, rev, data)
	appendSearchRecord(t, m, rev, now.Add(-2*time.Minute), nil)
	latest := appendSearchRecord(t, m, rev, *now, nil)
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	count := uint64(1)
	rows, _ := followSearch(t, Config{Root: c.Root, Start: time.UnixMilli(0), End: time.UnixMilli(1 << 48), Limit: &count, Tail: true})
	nextFollow(t, rows, latest)
	late := appendSearchRecord(t, m, rev, now.Add(-25*time.Hour), codec.Record{14: uint64(500)})
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	if row := nextFollow(t, rows, late); row["http_status"] != uint64(500) {
		t.Fatal(row)
	}
	older := appendSearchRecord(t, m, rev, now.Add(-time.Minute), codec.Record{3: uint64(3)})
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	if row := nextFollow(t, rows, older); row["level"] != "INFO" {
		t.Fatal(row)
	}
	definition := testDefinition()
	definition.Version = 43
	definition.Fields[3] = codec.Field{Name: "level", Enums: map[uint16]string{5: "FATAL"}}
	newRevision, definitions, err := definition.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Install(newRevision, definitions); err != nil {
		t.Fatal(err)
	}
	changed := appendSearchRecord(t, m, newRevision, *now, nil)
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	if row := nextFollow(t, rows, changed); row["level"] != "FATAL" {
		t.Fatal(row)
	}
	*now = now.Add(24 * time.Hour)
	nextDay := appendSearchRecord(t, m, rev, *now, nil)
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	nextFollow(t, rows, nextDay)
	c.Tap = "newtap"
	second := mustOpen(t, c, rev, data)
	newTap := appendSearchRecord(t, second, rev, *now, nil)
	if err := second.Flush(); err != nil {
		t.Fatal(err)
	}
	nextFollow(t, rows, newTap)
}

func TestFollowWaitsForCompleteBlock(t *testing.T) {
	c, rev, data, now := fixture(t)
	m := mustOpen(t, c, rev, data)
	first := appendSearchRecord(t, m, rev, *now, nil)
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	rows, _ := followSearch(t, Config{Root: c.Root, Start: time.UnixMilli(0), End: time.UnixMilli(1 << 48)})
	nextFollow(t, rows, first)
	dir := filepath.Join(c.Root, "0", fmt.Sprint(rev.Version), now.Format("2006/002"))
	index, err := os.OpenFile(filepath.Join(dir, "log.idx"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	stat, err := index.Stat()
	if err != nil {
		t.Fatal(err)
	}
	var previous [36]byte
	if _, err := index.ReadAt(previous[:], stat.Size()-36); err != nil {
		t.Fatal(err)
	}
	population, err := os.OpenFile(filepath.Join(dir, "log.pop"), os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer population.Close()
	stat, err = population.Stat()
	if err != nil {
		t.Fatal(err)
	}
	tail, err := codec.MarshalCompact(codec.Record{12: uint64(2234)}, codec.CBOR)
	if err != nil {
		t.Fatal(err)
	}
	block := func(data []byte) []byte {
		out := make([]byte, 8+len(data))
		binary.BigEndian.PutUint32(out, uint32(len(data)))
		binary.BigEndian.PutUint32(out[4:], crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli)))
		copy(out[8:], data)
		return out
	}
	if _, err := population.Write(block(tail)); err != nil {
		t.Fatal(err)
	}
	id, err := codec.NewUUIDv7(now.Add(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 40)
	binary.BigEndian.PutUint32(payload, 1)
	copy(payload[4:20], id[:])
	copy(payload[20:24], previous[16:20])
	binary.BigEndian.PutUint64(payload[24:], uint64(stat.Size()+8))
	binary.BigEndian.PutUint64(payload[32:], uint64(stat.Size()+8+int64(len(tail))))
	encoded := block(payload)
	if _, err := index.Seek(0, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Write(encoded[:20]); err != nil {
		t.Fatal(err)
	}
	select {
	case row := <-rows:
		t.Fatal("emitted incomplete block", row)
	case <-time.After(600 * time.Millisecond):
	}
	if _, err := index.Write(encoded[20:]); err != nil {
		t.Fatal(err)
	}
	nextFollow(t, rows, id.String())
	select {
	case row := <-rows:
		t.Fatal("replayed block", row)
	case <-time.After(350 * time.Millisecond):
	}
}

func TestFollowWaitsForNewTapAndHonorsEnd(t *testing.T) {
	c, rev, data, now := fixture(t)
	c.Tap = "later"
	q := Config{Root: c.Root, Tap: c.Tap, Start: now.Add(-time.Hour), End: now.Add(time.Minute)}
	rows, _ := followSearch(t, q)
	m := mustOpen(t, c, rev, data)
	atEnd := appendSearchRecord(t, m, rev, q.End, nil)
	matching := appendSearchRecord(t, m, rev, *now, nil)
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	nextFollow(t, rows, matching)
	select {
	case row := <-rows:
		t.Fatal("emitted excluded event", atEnd, row)
	case <-time.After(350 * time.Millisecond):
	}
}
