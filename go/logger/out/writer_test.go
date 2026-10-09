package out

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"trie-trunk/go/codec"
)

type stubConnection struct {
	current        codec.Revision
	wanted         codec.Revision
	definition     []byte
	events         []string
	negotiationErr error
	pushErr        error
	writeErr       error
	wrongAck       bool
	ready          bool
	closed         bool
	pushStarted    chan struct{}
	pushRelease    chan struct{}
}

func (c *stubConnection) Negotiate(ctx context.Context, wanted codec.Revision) (codec.Revision, error) {
	c.events = append(c.events, "negotiate")
	c.wanted = wanted
	c.ready = c.current == wanted
	return c.current, c.negotiationErr
}
func (c *stubConnection) PushSchema(ctx context.Context, wanted codec.Revision, data []byte) (codec.Revision, error) {
	c.events = append(c.events, "push")
	if c.pushStarted != nil {
		close(c.pushStarted)
		select {
		case <-c.pushRelease:
		case <-ctx.Done():
			return codec.Revision{}, ctx.Err()
		}
	}
	if c.pushErr != nil {
		return codec.Revision{}, c.pushErr
	}
	if _, err := decodeDefinitionFrame(data, wanted); err != nil {
		return codec.Revision{}, err
	}
	c.definition = append([]byte(nil), data...)
	if c.wrongAck {
		return codec.Revision{}, nil
	}
	c.current = wanted
	c.ready = true
	c.events = append(c.events, "ready")
	return wanted, nil
}
func (c *stubConnection) WriteFrame(data []byte) error {
	if !c.ready || c.closed {
		return errors.New("record before negotiation or after close")
	}
	payload, err := codec.ReadFrame(bytes.NewReader(data), codec.MaxFramePayload)
	if err != nil || len(payload) == 0 {
		return errors.New("invalid record frame")
	}
	c.events = append(c.events, "write")
	return c.writeErr
}
func (c *stubConnection) Close() error {
	c.closed = true
	c.events = append(c.events, "close")
	return nil
}

func testDefinition() codec.Definition {
	return codec.Define(2, codec.Schema{2: {Name: "category", Enums: map[uint16]string{1: "SUMMARY"}}}, nil)
}

func TestMatchingAndOutOfSyncSchemas(t *testing.T) {
	d := testDefinition()
	revision, _, _ := d.Prepare()
	for _, match := range []bool{true, false} {
		c := &stubConnection{}
		if match {
			c.current = revision
		}
		w, err := Open(context.Background(), ConnectFunc(func(context.Context) (Connection, error) { return c, nil }), d)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte{0xa1, 2, 1}); err != nil {
			t.Fatal(err)
		}
		want := []string{"negotiate", "write"}
		if !match {
			want = []string{"negotiate", "push", "ready", "write"}
		}
		if !reflect.DeepEqual(c.events, want) {
			t.Fatalf("events %v, want %v", c.events, want)
		}
		if w.Revision() != revision {
			t.Fatal("wrong active revision")
		}
	}
}

func TestHandshakeFailuresCloseWithoutWriting(t *testing.T) {
	failure := errors.New("transport failure")
	for _, c := range []*stubConnection{{negotiationErr: failure}, {pushErr: failure}, {wrongAck: true}} {
		w, err := Open(context.Background(), ConnectFunc(func(context.Context) (Connection, error) { return c, nil }), testDefinition())
		if w != nil || err == nil || !c.closed {
			t.Fatalf("handshake failure left usable connection: %v %+v", err, c)
		}
		for _, event := range c.events {
			if event == "write" {
				t.Fatal("wrote before schema acknowledgement")
			}
		}
	}
}

func TestFailureRequiresExplicitReconnectWithoutReplay(t *testing.T) {
	failure := errors.New("uncertain delivery")
	first := &stubConnection{writeErr: failure}
	second := &stubConnection{}
	calls := 0
	w, err := Open(context.Background(), ConnectFunc(func(context.Context) (Connection, error) {
		calls++
		if calls == 1 {
			return first, nil
		}
		second.current = first.current
		return second, nil
	}), testDefinition())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte{1}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte{2}); !errors.Is(err, ErrDisconnected) {
		t.Fatal(err)
	}
	if calls != 1 || !first.closed {
		t.Fatal("unexpected automatic reconnect or live failed connection")
	}
	if err := w.Reconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second.events, []string{"negotiate", "write"}) {
		t.Fatalf("replayed or unnecessarily pushed: %v", second.events)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte{4}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err := w.Reconnect(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestReconnectWaitsForSchemaBeforeConcurrentWrites(t *testing.T) {
	first := &stubConnection{}
	second := &stubConnection{pushStarted: make(chan struct{}), pushRelease: make(chan struct{})}
	calls := 0
	w, err := Open(context.Background(), ConnectFunc(func(context.Context) (Connection, error) {
		calls++
		if calls == 1 {
			return first, nil
		}
		return second, nil
	}), testDefinition())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- w.Reconnect(context.Background()) }()
	<-second.pushStarted
	var group sync.WaitGroup
	for i := 0; i < 16; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := w.Write([]byte{1}); err != nil {
				t.Error(err)
			}
		}()
	}
	close(second.pushRelease)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	group.Wait()
	if !first.closed || len(second.events) != 19 || !reflect.DeepEqual(second.events[:3], []string{"negotiate", "push", "ready"}) {
		t.Fatalf("incorrect reconnect ordering: %v", second.events)
	}
}

func TestFailedReconnectLeavesDisconnected(t *testing.T) {
	first := &stubConnection{}
	second := &stubConnection{wrongAck: true}
	calls := 0
	w, err := Open(context.Background(), ConnectFunc(func(context.Context) (Connection, error) {
		calls++
		if calls == 1 {
			return first, nil
		}
		return second, nil
	}), testDefinition())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Reconnect(context.Background()); !errors.Is(err, ErrSchemaAck) {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte{1}); !errors.Is(err, ErrDisconnected) {
		t.Fatal(err)
	}
	if !first.closed || !second.closed {
		t.Fatal("retired connections not closed")
	}
}

func TestOversizeRecordLeavesConnectionUsable(t *testing.T) {
	c := &stubConnection{}
	w, err := Open(context.Background(), ConnectFunc(func(context.Context) (Connection, error) { return c, nil }), testDefinition())
	if err != nil {
		t.Fatal(err)
	}
	before := len(c.events)
	if _, err := w.Write(make([]byte, codec.MaxFramePayload+1)); !errors.Is(err, codec.ErrFrameSize) {
		t.Fatal(err)
	}
	if c.closed || len(c.events) != before {
		t.Fatal("oversized record reached transport or retired connection")
	}
	if n, err := w.Write([]byte{0xa1, 2, 1}); err != nil || n != 3 {
		t.Fatalf("payload byte count or connection wrong: %d %v", n, err)
	}
}

func TestOversizeSchemaRejectedBeforeConnect(t *testing.T) {
	d := testDefinition()
	d.Fields[2] = codec.Field{Name: string(bytes.Repeat([]byte{'x'}, codec.MaxFramePayload))}
	calls := 0
	_, err := Open(context.Background(), ConnectFunc(func(context.Context) (Connection, error) { calls++; return &stubConnection{}, nil }), d)
	if !errors.Is(err, codec.ErrFrameSize) || calls != 0 {
		t.Fatalf("oversized schema connected: %d %v", calls, err)
	}
}

func decodeDefinitionFrame(frame []byte, revision codec.Revision) (codec.Definition, error) {
	payload, err := codec.ReadFrame(bytes.NewReader(frame), codec.MaxFramePayload)
	if err != nil {
		return codec.Definition{}, err
	}
	return codec.ParseDefinition(payload, revision)
}
