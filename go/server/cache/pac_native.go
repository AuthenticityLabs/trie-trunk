package cache

import (
	"fmt"
	"math"
)

// PACPlacement is Trie-Trunk's version-zero required extension. It copies source
// coordinates from the time index; it never certifies local completeness.
// The decoded address space contains packed members only, without PAC headers.
type PACPlacement struct {
	Source                   [16]byte
	Trie                     [32]byte
	Block                    uint32
	PageSize                 uint32
	From, To                 int64
	FirstRow                 uint64
	MemberStart, MemberBytes uint64
	Rows                     uint64
	Pages                    []PACPage
}
type PACPage struct{ Row, Page uint64 }

const pacPlacementHeader = 104

// PlacePAC runs after factoring, before recording lengths in the server index.
// It allows records and hours to cross pages with no alignment padding.
func PlacePAC(p *PAC, place PACPlacement) (PACPlacement, error) {
	if place.PageSize == 0 || place.From >= place.To {
		return place, fmt.Errorf("invalid PAC placement")
	}
	place.Rows = uint64(len(p.Members))
	if place.FirstRow > math.MaxUint64-place.Rows {
		return place, fmt.Errorf("PAC row overflow")
	}
	place.Pages = nil
	at := place.MemberStart
	for i, m := range p.Members {
		length := uint64(len(m))
		if length == 0 || uint64(place.PageSize)-1+length > math.MaxUint32 || at > math.MaxUint64-length {
			return place, fmt.Errorf("PAC member exceeds page coordinate range")
		}
		page := at / uint64(place.PageSize)
		if len(place.Pages) == 0 || place.Pages[len(place.Pages)-1].Page != page {
			place.Pages = append(place.Pages, PACPage{uint64(i), page})
		}
		at += length
	}
	place.MemberBytes = at - place.MemberStart
	return place, nil
}
func (p PACPlacement) Extension() (PACExtension, error) {
	if err := p.validate(); err != nil {
		return PACExtension{}, err
	}
	raw := make([]byte, pacPlacementHeader, pacPlacementHeader+16*len(p.Pages))
	copy(raw, p.Source[:])
	copy(raw[16:48], p.Trie[:])
	order.PutUint32(raw[48:52], p.Block)
	order.PutUint32(raw[52:56], p.PageSize)
	order.PutUint64(raw[56:64], uint64(p.From))
	order.PutUint64(raw[64:72], uint64(p.To))
	order.PutUint64(raw[72:80], p.FirstRow)
	order.PutUint64(raw[80:88], p.Rows)
	order.PutUint64(raw[88:96], p.MemberStart)
	order.PutUint64(raw[96:104], p.MemberBytes)
	raw = extended(raw, uint64(len(p.Pages)))
	for _, marker := range p.Pages {
		raw = order.AppendUint64(raw, marker.Row)
		raw = order.AppendUint64(raw, marker.Page)
	}
	return PACExtension{Type: PACNative, Required: true, Data: raw}, nil
}
func ParsePACPlacement(e PACExtension) (PACPlacement, error) {
	var p PACPlacement
	b := e.Data
	if e.Type != PACNative || !e.Required || len(b) < pacPlacementHeader+2 {
		return p, fmt.Errorf("invalid native PAC extension")
	}
	copy(p.Source[:], b[:16])
	copy(p.Trie[:], b[16:48])
	p.Block = order.Uint32(b[48:])
	p.PageSize = order.Uint32(b[52:])
	p.From = int64(order.Uint64(b[56:]))
	p.To = int64(order.Uint64(b[64:]))
	p.FirstRow = order.Uint64(b[72:])
	p.Rows = order.Uint64(b[80:])
	p.MemberStart = order.Uint64(b[88:])
	p.MemberBytes = order.Uint64(b[96:])
	at := pacPlacementHeader
	n, err := readExtended(b, &at)
	if err != nil || n > uint64((len(b)-at)/16) || n*16 != uint64(len(b)-at) {
		return p, fmt.Errorf("invalid PAC page markers")
	}
	p.Pages = make([]PACPage, n)
	for i := range p.Pages {
		p.Pages[i] = PACPage{order.Uint64(b[at:]), order.Uint64(b[at+8:])}
		at += 16
	}
	return p, p.validate()
}
func (p PACPlacement) validate() error {
	if p.PageSize == 0 || p.From >= p.To || p.FirstRow > math.MaxUint64-p.Rows || p.MemberStart > math.MaxUint64-p.MemberBytes || (p.Rows == 0) != (p.MemberBytes == 0) {
		return fmt.Errorf("invalid PAC placement bounds")
	}
	if p.Rows == 0 {
		if len(p.Pages) != 0 {
			return fmt.Errorf("empty PAC has page markers")
		}
		return nil
	}
	if len(p.Pages) == 0 || p.Pages[0].Row != 0 || p.Pages[0].Page != p.MemberStart/uint64(p.PageSize) {
		return fmt.Errorf("missing PAC starting page")
	}
	last := (p.MemberStart + p.MemberBytes - 1) / uint64(p.PageSize)
	for i, m := range p.Pages {
		if m.Row >= p.Rows || m.Page > last || (i > 0 && (m.Row <= p.Pages[i-1].Row || m.Page <= p.Pages[i-1].Page)) {
			return fmt.Errorf("invalid PAC page transition")
		}
	}
	return nil
}

// PACLocalRows validates immutable shared rows; it never changes their bytes.
func PACLocalRows(p *PAC, place PACPlacement, rows []byte) ([]byte, error) {
	expected, err := PlacePAC(p, place)
	if err != nil {
		return nil, err
	}
	if len(expected.Pages) != len(place.Pages) {
		return nil, fmt.Errorf("PAC page markers mismatch")
	}
	for i, p := range expected.Pages {
		if p != place.Pages[i] {
			return nil, fmt.Errorf("PAC page marker mismatch")
		}
	}
	if expected.Rows != place.Rows || expected.MemberBytes != place.MemberBytes || uint64(len(rows)) != place.Rows*IndexRowSize {
		return nil, fmt.Errorf("PAC index/placement mismatch")
	}
	if p.AddressBytes != 4 {
		return nil, fmt.Errorf("native PAC requires U32 directory")
	}
	for i := range p.Members {
		ms := indexTime(rows[i*IndexRowSize:], place.From)
		if ms < place.From || ms >= place.To {
			return nil, fmt.Errorf("PAC row time outside hour")
		}
	}

	return append([]byte(nil), rows...), nil
}
