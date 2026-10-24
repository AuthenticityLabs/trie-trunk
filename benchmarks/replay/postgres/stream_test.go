package main

import (
	"bytes"
	"reflect"
	"testing"
	"testing/iotest"
	"time"
	"trie-trunk/go/server/cache"
)

func TestStreamAtomicFramingAndSparseApplication(t *testing.T) {
	l := layout()
	state := fixture(1, 0, 4096)
	for revision := 1; revision < 10; revision++ {
		want := fixture(1, revision, 4096)
		raw := encode(l, want, 1, false)
		packet := raw
		seen := 0
		e := consumeUpdates(l, bytes.NewReader(packet), false, func(payload []byte) error {
			seen++
			var frame cache.Frame
			return apply(l, payload, &frame, &state)
		})
		if e != nil || seen != 1 || !reflect.DeepEqual(state, want) {
			t.Fatal("Stream patch changed semantics", e)
		}
		seen = 0
		e = consumeUpdates(l, bytes.NewReader(packet[:len(packet)-1]), false, func([]byte) error { seen++; return nil })
		if e == nil || seen != 0 {
			t.Fatal("truncated message applied")
		}
	}
}
func TestStreamSlowQueueFailsInsteadOfDropping(t *testing.T) {
	c := streamClients{queues: []chan []byte{make(chan []byte, 1)}}
	if e := c.publish([]byte("first")); e != nil {
		t.Fatal(e)
	}
	if e := c.publish([]byte("second")); e == nil {
		t.Fatal("silently accepted dropped event")
	}
	if string(<-c.queues[0]) != "first" {
		t.Fatal("overwrote queued event")
	}
}
func TestStreamPersistentClientsShareImmutablePacket(t *testing.T) {
	l := layout()
	initial := fixture(1, 0, 256)
	expected := []Object{initial, fixture(1, 1, 256)}
	clients, e := openStreamClients(l, initial, expected, 3, false)
	if e != nil {
		t.Fatal(e)
	}
	defer clients.close()
	packet := encode(l, expected[1], 1, false)
	original := bytes.Clone(packet)
	if e = clients.publish(packet); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		select {
		case ack := <-clients.acks:
			if ack.err != nil || ack.revision != 1 {
				t.Fatal(ack)
			}
		case <-time.After(time.Second):
			t.Fatal("missing delivery")
		}
	}
	if !bytes.Equal(packet, original) {
		t.Fatal("shared packet mutated")
	}
}

func TestStreamFragmentedAndCoalescedFrames(t *testing.T) {
	l := layout()
	initial := fixture(1, 0, 4096)
	var sequence []byte
	for rev := 1; rev <= 12; rev++ {
		sequence = append(sequence, encode(l, fixture(1, rev, 4096), 1, false)...)
	}
	for _, fragmented := range []bool{false, true} {
		state := initial
		seen := 0
		var frame cache.Frame
		reader := iotest.OneByteReader(bytes.NewReader(sequence))
		if !fragmented {
			reader = bytes.NewReader(sequence)
		}
		err := consumeUpdates(l, reader, false, func(raw []byte) error {
			seen++
			if err := apply(l, raw, &frame, &state); err != nil {
				return err
			}
			if !reflect.DeepEqual(state, fixture(1, seen, 4096)) {
				t.Fatalf("wrong state at %d", seen)
			}
			return nil
		})
		if err != nil || seen != 12 {
			t.Fatalf("framing: seen=%d error=%v", seen, err)
		}
	}
	raw := encode(l, fixture(1, 1, 4096), 1, true)
	for cut := 1; cut < len(raw); cut++ {
		seen := 0
		err := consumeUpdates(l, bytes.NewReader(raw[:cut]), false, func([]byte) error { seen++; return nil })
		if err == nil || seen != 0 {
			t.Fatalf("accepted truncated frame at %d", cut)
		}
	}
}
