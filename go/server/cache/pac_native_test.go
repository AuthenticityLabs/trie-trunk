package cache

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPACNativePlacementAndRows(t *testing.T) {
	l := testLayout(t, false)
	var frames [][]byte
	for i := 0; i < 16; i++ {
		frames = append(frames, testFrame(t, l, time.UnixMilli(int64(i)), uint32(i), false))
	}
	p, err := PackPAC(l, frames, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	// Begin partway through a page; neither records nor the next hour get padded.
	place, err := PlacePAC(p, PACPlacement{Source: [16]byte{1}, Trie: l.Digest, Block: 9, PageSize: 64, From: 0, To: 3600000, FirstRow: 77, MemberStart: 119})
	if err != nil {
		t.Fatal(err)
	}
	ext, err := place.Extension()
	if err != nil {
		t.Fatal(err)
	}
	p.Extensions = []PACExtension{ext}
	p.AddressBytes = 4
	p.Bytes, err = p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParsePAC(l, p.Bytes, map[uint32]bool{PACNative: true})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ParsePACPlacement(parsed.Extensions[0])
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]byte, IndexRowSize*len(frames))

	for i := range p.Members {
		var f Frame
		if e := l.Scan(frames[i], &f); e != nil {
			t.Fatal(e)
		}
		id, e := eventID(f.Values[l.Event])
		if e != nil {
			t.Fatal(e)
		}
		if e = packIndexID(rows[i*IndexRowSize:], id, place.From); e != nil {
			t.Fatal(e)
		}
	}
	local, err := PACLocalRows(p, decoded, rows)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(local, rows) {
		t.Fatal("index changed during replication")
	}
	at := place.MemberStart + place.MemberBytes
	next, err := PlacePAC(p, PACPlacement{PageSize: 64, From: 3600000, To: 7200000, MemberStart: at})
	if err != nil || next.MemberStart != place.MemberStart+place.MemberBytes {
		t.Fatal("hour alignment padding", err)
	}
	bad := append([]byte(nil), rows...)
	order.PutUint32(bad, 3600000)
	if _, err = PACLocalRows(p, place, bad); err == nil {
		t.Fatal("wrong source length accepted")
	}
	broken := place
	broken.Pages = append([]PACPage(nil), place.Pages...)
	broken.Pages[0].Page++
	if _, err = broken.Extension(); err == nil {
		t.Fatal("wrong starting page accepted")
	}
	if dir := os.Getenv("PIL_PAC_FIXTURE_DIR"); dir != "" {
		for name, b := range map[string][]byte{"native.pac": p.Bytes, "native.idx": rows, "local.idx": local} {
			if err = os.WriteFile(filepath.Join(dir, name), b, 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
