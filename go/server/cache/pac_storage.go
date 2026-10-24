package cache

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"math"
	"time"
)

// StoredPAC is the block directory entry in the companion commit journal. Population
// addresses are encoded extents; placement addresses count decoded members only.
// Index rows keep decoded member starts; the segment directory owns compressed extents.
type StoredPAC struct {
	Placement PACPlacement `cbor:"0,keyasint"`
	Decoded   uint64       `cbor:"1,keyasint"`
	Gzip      bool         `cbor:"2,keyasint"`
}

const NativePageSize = 1 << 20

func (s *Store) packBlock(b *branchStore, first uint64, batch []pending, frames [][]byte, rows []byte) ([]byte, *StoredPAC, error) {
	if uint64(b.blockBase)+uint64(len(b.runs)) > math.MaxUint32 {
		return nil, nil, fmt.Errorf("population block number exhausted")
	}
	p, err := factorPAC(s.layout, frames, nil, true)
	if err != nil {
		return nil, nil, err
	}
	start := b.memberEnd
	if len(b.runs) > 0 {
		last := b.runs[len(b.runs)-1].PAC.Placement
		start = last.MemberStart + last.MemberBytes
	}
	from, to := semanticBounds(time.UnixMilli(eventTime(batch[0].id[:])), 1, s.location)
	// The shared trie is append-only. Bind the exact prefix needed by these rows.
	trie, err := encoder.Marshal(s.trie.nodes)
	if err != nil {
		return nil, nil, err
	}
	place, err := PlacePAC(p, PACPlacement{
		Source: b.generation, Trie: sha256.Sum256(trie), Block: b.blockBase + uint32(len(b.runs)),
		PageSize: NativePageSize, From: from.UnixMilli(), To: to.UnixMilli(),
		FirstRow: first, MemberStart: start + 4,
	})
	if err != nil {
		return nil, nil, err
	}
	if eventTime(batch[len(batch)-1].id[:]) >= place.To {
		return nil, nil, fmt.Errorf("PAC crosses an hour boundary")
	}
	e, err := place.Extension()
	if err != nil {
		return nil, nil, err
	}
	p.Extensions = []PACExtension{{Type: PACSource, Data: []byte((&HTTP{Store: s}).tap(b.branch))}, e}
	p.Bytes, err = p.marshal()
	if err != nil {
		return nil, nil, err
	}
	var decodedEnd uint64
	for i, m := range p.Members {
		order.PutUint32(rows[i*IndexRowSize+12:], uint32(decodedEnd))
		decodedEnd += uint64(len(m))
		if decodedEnd > math.MaxUint32-4 {
			return nil, nil, fmt.Errorf("decoded segment exceeds U32")
		}
	}

	block := &StoredPAC{Placement: place, Decoded: uint64(len(p.Bytes)), Gzip: b.branch != Write}
	if block.Gzip {
		sealed, err := compressPAC(p.Bytes, s.policyFor(b.branch).PAC.level())
		if err == nil {
			block.Gzip = order.Uint32(sealed[4:8])&2 != 0
		}
		return sealed, block, err
	}
	return p.Bytes, block, nil
}

func validateBlock(run Run, rows []byte, layout RowLayout, source [16]byte) error {
	if run.PAC == nil {
		return fmt.Errorf("missing PAC directory")
	}
	p := run.PAC.Placement
	if err := p.validate(); err != nil {
		return err
	}
	if p.Source != source || p.FirstRow != run.Start || p.Rows != run.End-run.Start ||
		len(rows) != int(p.Rows)*layout.Width || run.PAC.Decoded > PACMaximum ||
		run.PAC.Decoded < PACHeaderSize || run.MinMS < p.From || run.MaxMS >= p.To {
		return fmt.Errorf("PAC directory/commit mismatch")
	}
	at, marker, previous := p.MemberStart, 0, uint64(math.MaxUint64)
	for i := 0; i < len(rows); i += layout.Width {
		row := rows[i : i+layout.Width]
		if i > 0 && bytes.Compare(rows[i-layout.Width:i-layout.Width+12], row[:12]) >= 0 {
			return fmt.Errorf("unordered PAC rows")
		}
		start := uint64(order.Uint32(row[12:16]))
		end := p.MemberBytes
		if i+layout.Width < len(rows) {
			end = uint64(order.Uint32(rows[i+layout.Width+12:]))
		}
		length := end - (at - p.MemberStart)
		ms := p.From + indexDelta(row)
		if start != at-p.MemberStart || end <= start || length == 0 ||
			length > MaxFrame || ms < run.MinMS || ms > run.MaxMS {
			return fmt.Errorf("invalid PAC row")
		}
		page := at / uint64(p.PageSize)
		if page != previous {
			if marker >= len(p.Pages) || p.Pages[marker] != (PACPage{uint64(i / layout.Width), page}) {
				return fmt.Errorf("PAC page directory mismatch")
			}
			marker++
			previous = page
		}
		at += length
	}
	if at-p.MemberStart != p.MemberBytes || marker != len(p.Pages) {
		return fmt.Errorf("PAC population length mismatch")
	}
	return nil
}

// Only payload inspection uses this decoder. Replication reads the encoded
// extent directly and never calls this function. Keep one decoded block per
// query, rather than decompressing separately for each matching event.
func decodeStoredPAC(l *Layout, run Run, raw []byte) (*PAC, error) {
	if run.PAC == nil {
		return nil, fmt.Errorf("missing PAC directory")
	}
	var err error
	raw, err = expandPAC(raw)
	if err != nil {
		return nil, err
	}

	if uint64(len(raw)) != run.PAC.Decoded {
		return nil, fmt.Errorf("decoded PAC length mismatch")
	}
	p, err := ParsePAC(l, raw, map[uint32]bool{PACNative: true})
	if err != nil {
		return nil, err
	}
	if uint64(len(p.Members)) != run.End-run.Start {
		return nil, fmt.Errorf("PAC member count mismatch")
	}
	return p, nil
}

// The compressed representation has only framing/decoding controls outside gzip.
// Its body expands to the original PAC header, extensions, prefix, and records.
func compressPAC(raw []byte, level int) ([]byte, error) {
	if err := validatePlainPACEnvelope(raw); err != nil {
		return nil, err
	}
	var body bytes.Buffer
	z, err := gzip.NewWriterLevel(&body, level)
	if err != nil {
		return nil, err
	}
	if _, err = z.Write(raw); err != nil {
		return nil, err
	}
	if err = z.Close(); err != nil {
		return nil, err
	}
	if body.Len()+PACCompressedHeaderSize >= len(raw) {
		return raw, nil
	}
	out := make([]byte, PACCompressedHeaderSize, PACCompressedHeaderSize+body.Len())
	order.PutUint32(out[0:4], uint32(PACCompressedHeaderSize+body.Len()))
	order.PutUint32(out[4:8], PACGzip)
	order.PutUint32(out[8:12], uint32(len(raw)))
	order.PutUint32(out[12:16], uint32(order.Uint64(raw[24:32])))
	return append(out, body.Bytes()...), nil
}

func validatePlainPACEnvelope(raw []byte) error {
	if len(raw) < PACHeaderSize || uint64(len(raw)) > PACMaximum ||
		uint64(order.Uint32(raw[:4])) != uint64(len(raw)) || order.Uint32(raw[4:8]) > PACCommon ||
		order.Uint64(raw[8:16]) != uint64(len(raw)) || order.Uint64(raw[16:24]) < PACHeaderSize ||
		order.Uint64(raw[16:24]) > uint64(len(raw)) || order.Uint64(raw[24:32]) > PACMaximumMembers {
		return fmt.Errorf("invalid decoded PAC envelope")
	}
	return nil
}

func expandPAC(raw []byte) ([]byte, error) {
	if len(raw) < 8 || uint64(order.Uint32(raw[:4])) != uint64(len(raw)) {
		return nil, fmt.Errorf("invalid PAC stored length")
	}
	flags := order.Uint32(raw[4:8])
	if flags <= PACCommon {
		if err := validatePlainPACEnvelope(raw); err != nil {
			return nil, err
		}
		return raw, nil
	}
	if flags != PACGzip || len(raw) <= PACCompressedHeaderSize {
		return nil, fmt.Errorf("invalid PAC compression controls")
	}
	decoded, count := order.Uint32(raw[8:12]), order.Uint32(raw[12:16])
	if decoded < PACHeaderSize || uint64(count) > uint64(decoded)-PACHeaderSize {
		return nil, fmt.Errorf("invalid PAC compression bounds")
	}
	input := bytes.NewReader(raw[PACCompressedHeaderSize:])
	z, err := gzip.NewReader(input)
	if err != nil {
		return nil, err
	}
	z.Multistream(false)
	out, err := io.ReadAll(io.LimitReader(z, int64(decoded)+1))
	closeErr := z.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if uint64(len(out)) != uint64(decoded) || input.Len() != 0 {
		return nil, fmt.Errorf("invalid decoded PAC length or trailing compressed data")
	}
	if err := validatePlainPACEnvelope(out); err != nil {
		return nil, err
	}
	if order.Uint64(out[24:32]) != uint64(count) {
		return nil, fmt.Errorf("PAC compressed/decoded count mismatch")
	}
	return out, nil
}
