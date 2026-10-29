package cache

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// Test-side client: fetch complete native hours, then resolve selected records.
// decodeView alone continues to expose exactly what /events sent on the wire.
func collectView(t *testing.T, l *Layout, h *HTTP, raw []byte) viewDecoded {
	t.Helper()
	d := decodeView(t, l, raw)
	var resolved [][]byte
	live := 0
	for _, ref := range d.result.Page {
		if ref.Cached {
			continue
		}
		if ref.Native == nil {
			resolved = append(resolved, d.frames[live])
			live++
			continue
		}
		binding := d.bindings[ref.Stream]
		q := url.Values{"format": {"native"}, "tap": {binding}, "definition": {fmtDigestNative(l.Digest)}, "from": {time.UnixMilli(ref.Native.From).Format(time.RFC3339Nano)}, "to": {time.UnixMilli(ref.Native.To).Format(time.RFC3339Nano)}}
		r := httptest.NewRequest("GET", "/collect?"+q.Encode(), nil)
		r.Header.Set("Accept", "application/pil")
		r.Header.Set("PIL-Version", "2")
		r.Header.Set("PIL-Features", "native-ranges,adaptive-integers,native-state-pages")
		r.Header.Set("Accept-Encoding", "gzip")
		if h.Token != "" {
			r.Header.Set("Authorization", "Bearer "+h.Token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		_, chunks := readNativeTest(t, w.Body.Bytes())
		trie := sharedTrie{nodes: []sharedNode{{}}}
		var row []byte
		var pac *PAC
		var place PACPlacement
		for _, chunk := range chunks {
			switch chunk.Kind {
			case "trie":
				var nodes []sharedNode
				if e := decoder.Unmarshal(chunk.Data, &nodes); e != nil {
					t.Fatal(e)
				}
				if chunk.Start != uint64(len(trie.nodes)) {
					t.Fatal("trie order")
				}
				trie.nodes = append(trie.nodes, nodes...)
			case "index":
				if ref.Row >= chunk.Start && ref.Row < chunk.Start+uint64(len(chunk.Data)/IndexRowSize) {
					at := (ref.Row - chunk.Start) * IndexRowSize
					row = bytes.Clone(chunk.Data[at : at+IndexRowSize])
				}
			case "pac":
				decoded, e := expandPAC(chunk.Data)
				if e != nil {
					t.Fatal(e)
				}
				p, e := ParsePAC(l, decoded, map[uint32]bool{PACNative: true})
				if e != nil {
					t.Fatal(e)
				}
				for _, ext := range p.Extensions {
					if ext.Type == PACNative {
						position, e := ParsePACPlacement(ext)
						if e != nil {
							t.Fatal(e)
						}
						if ref.Row >= position.FirstRow && ref.Row < position.FirstRow+position.Rows {
							pac = p
							place = position
						}
					}
				}
			}
		}
		if row == nil || pac == nil {
			t.Fatal("missing native row/PAC", fmt.Sprint(ref.Row))
		}
		member, e := ResolvePAC(l, pac.Common, pac.Members[ref.Row-place.FirstRow])
		if e != nil {
			t.Fatal(e)
		}
		prefix := timeKey(indexTime(row, place.From))
		a := frameAssembler{layout: l, trie: &trie}
		full, e := a.assemble(prefix, member, nil, order.Uint16(row[4:6]))
		if e != nil {
			t.Fatal(e)
		}
		resolved = append(resolved, full)
	}
	if live != len(d.frames) {
		t.Fatal("unreferenced live member")
	}
	d.frames = resolved
	return d
}
