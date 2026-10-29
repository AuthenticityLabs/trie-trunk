package main

import (
	"bytes"
	"testing"
	"time"

	"trie-trunk/go/codec"
)

func TestWeek(t *testing.T) {
	d := definition()
	for _, key := range d.Path {
		if d.Fields[key].Name == "message" {
			t.Fatal("message must remain in the per-event payload")
		}
	}
	end := time.Date(2026, 9, 26, 21, 0, 0, 0, time.UTC)
	events := week(end)
	repeated := week(end)
	if len(events) > 5000 || len(events) < 3000 {
		t.Fatal(len(events))
	}
	seen := make(map[codec.UUIDv7]bool)
	markers, incident, failed, ordinary, ordinaryFailed := 0, 0, 0, 0, 0
	for i, e := range events {
		id := e.fields[0].(codec.UUIDv7)
		if !id.Valid() || seen[id] {
			t.Fatal("invalid or repeated uuid", i)
		}
		seen[id] = true
		if e.at.Before(end.Add(-168*time.Hour)) || !e.at.Before(end) {
			t.Fatal(e.at)
		}
		if i > 0 && e.at.Before(events[i-1].at) {
			t.Fatal("out of order")
		}
		a, err := codec.MarshalCompact(e.fields, codec.PIL)
		if err != nil {
			t.Fatal(err)
		}
		b, err := codec.MarshalCompact(repeated[i].fields, codec.PIL)
		if err != nil || !bytes.Equal(a, b) {
			t.Fatal("nondeterministic sample", err)
		}
		if e.fields[12] != true {
			t.Fatal("missing synthetic marker")
		}
		if e.fields[6] != uint16(1) {
			markers++
			continue
		}
		minute := int(e.at.Sub(end.Add(-168*time.Hour)) / time.Minute)
		if minute >= 26*60 && minute < 27*60+30 {
			incident++
			if e.fields[2] == uint16(3) {
				failed++
			}
		} else if minute < 26*60 {
			ordinary++
			if e.fields[2] == uint16(3) {
				ordinaryFailed++
			}
		}
	}
	if markers != len(milestones) {
		t.Fatal(markers)
	}
	if failed*100/incident < 40 {
		t.Fatal("incident not visible", failed, incident)
	}
	if ordinaryFailed*100/ordinary > 5 {
		t.Fatal("baseline too noisy")
	}
}
