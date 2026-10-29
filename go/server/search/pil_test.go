package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"trie-trunk/go/codec"
)

func TestPILSearchMatchesJSONAcrossCatalogs(t *testing.T) {
	c, _, _, now := fixture(t)
	var known []string
	for i, tap := range []string{"east", "west"} {
		c.Tap = tap
		d := testDefinition()
		d.Version = 42
		d.Fields[3] = codec.Field{Name: "level", Enums: map[uint16]string{5: "LABEL_" + tap}}
		d.Domains = map[string]map[uint16]string{"tier": {1: "PREMIUM"}}
		d.Fields[22] = codec.Field{Name: "items", Array: codec.Schema{1: {Name: "tier", Domain: "tier"}}}
		rev, data, err := d.Prepare()
		if err != nil {
			t.Fatal(err)
		}
		known = append(known, fmt.Sprintf("%x", rev.Digest))
		m := mustOpen(t, c, rev, data)
		for n := 0; n < 3; n++ {
			extra := codec.Record{2: uint64(1), 21: map[string]any{"null": nil, "bytes": []byte{0, 1, 255}, "empty": ""}, 22: []codec.Record{{1: uint64(1)}}}
			if n == 1 {
				extra[3] = "literal-level"
			}
			if n == 2 {
				extra[3] = "custom"
				delete(extra, 2)
			}
			appendSearchRecord(t, m, rev, now.Add(time.Duration(n*2+i)*time.Millisecond), extra)
		}
		if err := m.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	for _, pretty := range []bool{false, true} {
		q := Config{Root: c.Root, Start: *now, End: now.Add(time.Second), Pretty: pretty, Headers: []string{"duration", "level"}}
		var expected, schemas, wire, output bytes.Buffer
		if err := Run(context.Background(), q, &expected); err != nil {
			t.Fatal(err)
		}
		if err := PILDefinitions(context.Background(), q.Root, "", nil, &schemas); err != nil {
			t.Fatal(err)
		}
		var cached bytes.Buffer
		if err := PILDefinitions(context.Background(), q.Root, "", known, &cached); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(cached.Bytes(), []byte("TPIL\x01\xff")) {
			t.Fatal("fresh dictionaries were resent")
		}
		q.PIL, q.PILKnown = true, known
		if err := Run(context.Background(), q, &wire); err != nil {
			t.Fatal(err)
		}
		for _, label := range []string{"LABEL_east", "LABEL_west", "PREMIUM", "trunk_fallback", "dictionary"} {
			if bytes.Contains(wire.Bytes(), []byte(label)) {
				t.Fatal("event transfer expanded/repeated dictionary", label)
			}
		}
		combined := append(append([]byte{}, schemas.Bytes()...), wire.Bytes()...)
		if err := DecodePIL(context.Background(), q, io.NopCloser(bytes.NewReader(combined)), &output); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(output.Bytes(), expected.Bytes()) {
			t.Fatalf("PIL output differs\n%s\nexpected\n%s", output.String(), expected.String())
		}
		output.Reset()
		if err := DecodePIL(context.Background(), q, io.NopCloser(bytes.NewReader(combined[:len(combined)-1])), &output); err == nil {
			t.Fatal("missing stream terminator accepted")
		}
		if err := DecodePIL(context.Background(), q, io.NopCloser(bytes.NewReader(wire.Bytes())), io.Discard); err == nil {
			t.Fatal("unnegotiated schema accepted")
		}
	}
}

func TestPILFollowIntroducesNewDefinitionBeforeNewEvents(t *testing.T) {
	c, rev, data, now := fixture(t)
	m := mustOpen(t, c, rev, data)
	appendSearchRecord(t, m, rev, *now, nil)
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	q := Config{Root: c.Root, Start: *now, End: now.Add(time.Hour), Follow: true, PIL: true, PILKnown: []string{fmt.Sprintf("%x", rev.Digest)}}
	var initial bytes.Buffer
	if err := PILDefinitions(context.Background(), q.Root, "", nil, &initial); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw, rawWrite := io.Pipe()
	rendered, renderedWrite := io.Pipe()
	defer raw.Close()
	defer rendered.Close()
	encodeDone := make(chan error, 1)
	decodeDone := make(chan error, 1)
	go func() { err := Run(ctx, q, rawWrite); rawWrite.CloseWithError(err); encodeDone <- err }()
	go func() {
		err := DecodePIL(ctx, q, &pilInput{Reader: io.MultiReader(bytes.NewReader(initial.Bytes()), raw), body: raw}, renderedWrite)
		renderedWrite.CloseWithError(err)
		decodeDone <- err
	}()
	received := make(chan map[string]any, 2)
	go func() {
		dec := json.NewDecoder(rendered)
		for {
			var event map[string]any
			if dec.Decode(&event) != nil {
				return
			}
			received <- event
		}
	}()
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("initial event missing")
	}
	d := testDefinition()
	d.Version++
	d.Fields[3] = codec.Field{Name: "level", Enums: map[uint16]string{5: "NEW_SCHEMA"}}
	newRev, newData, err := d.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Install(newRev, newData); err != nil {
		t.Fatal(err)
	}
	appendSearchRecord(t, m, newRev, now.Add(time.Millisecond), nil)
	if err = m.Flush(); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-received:
		if event["level"] != "NEW_SCHEMA" {
			t.Fatal(event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("new schema event missing")
	}
	cancel()
	rendered.Close()
	raw.Close()
	for _, done := range []chan error{encodeDone, decodeDone} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("PIL follow leaked process")
		}
	}
}

func TestPILDirectSearchPreservesPayloadAndFilterSemantics(t *testing.T) {
	for _, maxNodes := range []int{1, 65536} {
		t.Run(fmt.Sprint(maxNodes), func(t *testing.T) {
			c, _, _, now := fixture(t)
			c.MaxNodes = maxNodes
			d := testDefinition()
			d.Fields[15] = codec.Field{Name: "message"}
			d.Fields[22] = codec.Field{Name: "amount"}
			d.Fields[23] = codec.Field{Name: "reference", Encoding: "uuidv7"}
			rev, data, err := d.Prepare()
			if err != nil {
				t.Fatal(err)
			}
			m := mustOpen(t, c, rev, data)
			for i, extra := range []codec.Record{
				{15: "wanted", 21: map[string]any{"text": "héllo 世界", "bytes": []byte{0, 255}, "items": []any{nil, true, int64(-5), float64(1.25)}}, 22: ^uint64(0)},
				{3: uint64(3), 15: "other", 21: map[string]any{}, 22: uint64(0), 23: uint64(42)},
				{3: "literal", 21: map[string]any{"null": nil}},
				{3: uint64(3), 15: "", 22: int64(-1)},
				{15: "wanted", 22: uint64(42)},
			} {
				appendSearchRecord(t, m, rev, now.Add(time.Duration(i)*time.Millisecond), extra)
			}
			if err := m.Flush(); err != nil {
				t.Fatal(err)
			}
			limit := uint64(2)
			for _, where := range [][]string{nil, {`level=="ERROR"`}, {`message=="wanted"`}, {`message is null`}, {`message is !null`}, {`amount==18446744073709551615`}, {`level=="ERROR" message=="wanted"`, `level=="literal"`}} {
				for _, tail := range []bool{false, true} {
					q := Config{Root: c.Root, Start: *now, End: now.Add(time.Second), Where: where, Tail: tail, Limit: &limit}
					var expected, wire, actual bytes.Buffer
					if err := Run(context.Background(), q, &expected); err != nil {
						t.Fatal(err)
					}
					q.PIL = true
					if err := Run(context.Background(), q, &wire); err != nil {
						t.Fatal(err)
					}
					if err := DecodePIL(context.Background(), q, io.NopCloser(bytes.NewReader(wire.Bytes())), &actual); err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(expected.Bytes(), actual.Bytes()) {
						t.Fatalf("different output for where=%v tail=%v", where, tail)
					}
				}
			}
		})
	}
}
