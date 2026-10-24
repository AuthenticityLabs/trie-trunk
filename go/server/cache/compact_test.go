package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"testing"
	"time"
)

func TestCompactRowsAndEncodedAssembly(t *testing.T) {
	for _, variable := range []bool{false, true} {
		l := testLayout(t, variable)
		d := l.Definition
		// Event identity need not be the first definition field. Exercise offset
		// tables, arrays, CBOR, literal dictionary escapes and all presence states.
		d.Fields[0], d.Fields[5] = d.Fields[5], d.Fields[0]
		d.Fields = append(d.Fields, Field{Name: "tags", Type: "ARRAY<STRING>"}, Field{Name: "details", Type: "CBOR"})
		var err error
		l, err = Compile(d)
		if err != nil {
			t.Fatal(err)
		}
		if l.RowLayout().Width != IndexRowSize {
			t.Fatal("identity types changed index width")
		}
		at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
		s, err := Open(Config{Root: t.TempDir(), Cache: "test", Tenant: "test", Layout: l, BatchRecords: 2, Now: func() time.Time { return at.Add(72 * time.Hour) }})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		var originals [][]byte
		for i := 0; i < 8; i++ {
			base := testFrame(t, testLayout(t, variable), at.Add(time.Duration(i)*time.Minute), uint32(i), false)
			var f Frame
			if err = testLayout(t, variable).Scan(base, &f); err != nil {
				t.Fatal(err)
			}
			f.Values[0], f.Values[5] = f.Values[5], f.Values[0]
			tags := extended(nil, 2)
			tags = extended(tags, 0)
			tags = extended(tags, 3)
			tags = append(tags, []byte("abc")...)
			f.Values = append(f.Values, Value{Offset, tags}, Value{Offset, []byte{0xa1, 0x01, 0xf6}})
			switch i {
			case 0:
				f.Values[l.Object] = Value{}
				f.Values[l.Revision] = Value{State: Null}
			case 1:
				f.Values[l.Object] = Value{State: Null}
				f.Values[l.Revision] = Value{}
			case 2:
				f.Values[4] = Value{}
				f.Values[0] = Text("")
			case 3:
				f.Values[4] = Text("new literal state")
				f.Values[0] = Value{State: Null}
			case 4, 5:
				f.Values[0] = Text(string(bytes.Repeat([]byte{'x'}, 65530+i)))
			}
			raw, e := l.Encode(nil, f.Values)
			if e != nil {
				t.Fatal(e)
			}
			originals = append(originals, raw)
		}
		// Late commits interleave logical time while preserving physical pairing.
		for _, i := range []int{2, 6, 0, 4, 1, 7, 3, 5} {
			if _, err = s.Append(Write, originals[i]); err != nil {
				t.Fatal(err)
			}
		}
		if err = s.Flush(); err != nil {
			t.Fatal(err)
		}
		b := s.branches[Write]
		assembler := frameAssembler{layout: l, trie: &s.trie}
		var reader populationReader
		last := uint64(headerSize)
		for n := b.nextRecord(0); n < b.tail(); n = b.nextRecord(n + 1) {
			start, end, e := b.populationRange(n, s.row)
			if e != nil || (n == b.runs[b.runIndex(n)].Start && start != last) {
				t.Fatalf("physical bounds %d %d %v", start, last, e)
			}
			last = end
			raw, e := reader.read(b, n, s.row)
			if e != nil {
				t.Fatal(e)
			}
			var f Frame
			if e = l.Scan(raw, &f); e != nil {
				t.Fatal(e)
			}
			row := b.assemblyRow(uint64(n))
			want, e := s.expandFrame(row, &f, nil)
			if e != nil {
				t.Fatal(e)
			}
			got, e := assembler.assemble(row, raw, nil)
			if e != nil || !bytes.Equal(got, want) {
				t.Fatalf("encoded assembly: %v", e)
			}
			var full Frame
			if e = l.Scan(got, &full); e != nil {
				t.Fatal(e)
			}
			var stored Frame
			if e = l.Scan(raw, &stored); e != nil {
				t.Fatal(e)
			}
			for _, i := range []int{l.Object, l.Revision} {
				if !l.equal(i, stored.Values[i], full.Values[i]) {
					t.Fatal("identity missing from population")
				}
			}
		}
		if reader.reads != uint64(len(b.runs)) {
			t.Fatalf("contiguous population read %d times", reader.reads)
		}
		got := collect(t, s, Write, at, at.Add(time.Hour))
		for i := range originals {
			if !bytes.Equal(got[i], originals[i]) {
				t.Fatalf("late-run result %d", i)
			}
		}
		got = collect(t, s, Write, at.Add(3*time.Minute), at.Add(6*time.Minute))
		for i := range got {
			if !bytes.Equal(got[i], originals[i+3]) {
				t.Fatal("mid-run start used preceding match")
			}
		}
	}
}

func TestNativeCompactSliceBoundaries(t *testing.T) {
	l := testLayout(t, false)
	d := l.Definition
	d.Resource.Aggregations = nil
	l, err := Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	s, err := Open(Config{Root: t.TempDir(), Cache: "test", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(72 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Enough rows to force an index chunk boundary as well as cached gaps.
	const count = 41000
	for start := 0; start < count; start += 1024 {
		var frames [][]byte
		for i := start; i < min(start+1024, count); i++ {
			frames = append(frames, testFrame(t, l, at.Add(time.Duration(i)*time.Millisecond), uint32(i), i%2 == 0))
		}
		if _, err = s.AppendBatch(Write, frames); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Flush(); err != nil {
		t.Fatal(err)
	}
	b := s.branches[Write]
	request := CollectionRequest{Branch: Write, At: at}
	var m Manifest
	run := func() int {
		t.Helper()
		received := 0
		e := s.Collect(context.Background(), request, func(v Manifest) error {
			m = v
			return nil
		}, func(c NativeChunk) error {
			if sha256.Sum256(c.Data) != c.Digest {
				t.Fatal("digest mismatch")
			}
			switch c.Kind {
			case "index":
				if !bytes.Equal(c.Data, b.rows[c.Start*IndexRowSize:c.Start*IndexRowSize+uint64(len(c.Data))]) {
					t.Fatal("index changed")
				}

				received += len(c.Data) / IndexRowSize
			case "pac":
				run := b.runs[c.PAC.Placement.Block]
				raw, e := readBytes(b.pop, run.PopStart, run.PopEnd)
				if e != nil || !bytes.Equal(raw, c.Data) {
					t.Fatal("native population changed")
				}
			}
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
		return received
	}
	if got := run(); got != count || m.Provisional {
		t.Fatalf("initial collection %d %+v", got, m)
	}
	request.Have = Coverage{Definition: m.Definition, Generation: m.Generation, TrieNodes: m.TrieNodes, Index: []Span{{0, 2}, {5, 9}, {20, 25}}}
	if got := run(); got != count {
		t.Fatalf("gap fill count %d", got)
	}
	request.Have.Index = m.Index
	request.Have.Population = m.Population
	if got := run(); got != 0 {
		t.Fatal("cached rows resent")
	}
	// Finalized rows cannot be backfilled, even if the package remains provisional.
	if _, err = s.Append(Write, testFrame(t, l, at.Add(500*time.Millisecond), 999999, true)); err == nil {
		t.Fatal("late event accepted")
	}
	if got := run(); got != 0 {
		t.Fatalf("rejected event altered collection: %d", got)
	}
}
