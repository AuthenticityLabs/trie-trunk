package cache

// PAC is a version-zero representation, not a transport/session binding.
// Compression wraps the complete decoded PAC, including its semantic header.
import (
	"bytes"
	"fmt"
	"io"
	"math"
	"unicode/utf8"
)

const PACHeaderSize = 64
const PACCompressedHeaderSize = 16
const PACCommon = uint32(1)
const PACGzip = uint32(2)
const PACSource = uint32(1)          // optional UTF-8 source name, scoped by definition digest
const PACNative = uint32(0x5454524b) // required Trie-Trunk native placement binding
const PACMaximum = math.MaxUint32
const PACMaximumMembers = math.MaxUint32

type PACExtension struct {
	Type     uint32
	Required bool
	Data     []byte
}

type PAC struct {
	Digest     [32]byte
	Common     []byte
	Members    [][]byte
	Extensions []PACExtension
	Bytes      []byte
}

// PackPAC factors only equal, present positional values. No semantic map, trie
// expansion, reordered records, or changed ABSENT/NULL semantics.
func PackPAC(l *Layout, frames [][]byte, extensions []PACExtension, prefix bool) (*PAC, error) {
	p, err := factorPAC(l, frames, extensions, prefix)
	if err != nil {
		return nil, err
	}
	p.Bytes, err = p.marshal()
	return p, err
}

// Storage assigns native placement after factoring, then serializes exactly once.
func factorPAC(l *Layout, frames [][]byte, extensions []PACExtension, prefix bool) (*PAC, error) {
	if len(frames) > PACMaximumMembers {
		return nil, fmt.Errorf("PAC member limit")
	}
	var common []Value
	var active []int
	var scan Frame
	total := 0
	for i, raw := range frames {
		if err := l.scanPlain(raw, &scan); err != nil {
			return nil, err
		}
		if len(raw) > PACMaximum-total {
			return nil, fmt.Errorf("PAC exceeds byte limit")
		}
		total += len(raw)
		if prefix && len(frames) > 1 {
			if i == 0 {
				common = append([]Value(nil), scan.Values...)
				active = make([]int, 0, len(common))
				for f, v := range common {
					if v.State != Absent {
						active = append(active, f)
					}
				}
			} else {
				for i := 0; i < len(active); {
					f := active[i]
					v := common[f]
					if v.State != scan.Values[f].State || !bytes.Equal(v.Bytes, scan.Values[f].Bytes) {
						common[f] = Value{}
						active[i] = active[len(active)-1]
						active = active[:len(active)-1]
					} else {
						i++
					}
				}
			}
		}
	}
	p := &PAC{Digest: l.Digest, Members: frames, Extensions: extensions}
	if len(active) > 0 {
		var err error
		p.Common, err = l.Encode(nil, common)
		if err != nil {
			return nil, err
		}
		packed := make([][]byte, len(frames))
		arena := make([]byte, 0, total)
		var scratch []byte
		size := len(p.Common)
		for i, raw := range frames {
			if err = l.scanPlain(raw, &scan); err != nil {
				return nil, err
			}
			for f, v := range common {
				if v.State != Absent {
					scan.Values[f] = Value{}
				}
			}
			scratch, err = l.Encode(scratch, scan.Values)
			if err != nil {
				return nil, err
			}
			start := len(arena)
			arena = append(arena, scratch...)
			packed[i] = arena[start:len(arena)]
			size += len(packed[i])
		}
		// Include the variable common-length prefix in the decision.
		if size+len(extended(nil, uint64(len(p.Common)))) < total+2 {
			p.Members = packed
		} else {
			p.Common = nil
		}
	}
	return p, nil
}

func (p *PAC) marshal() ([]byte, error) {
	h := make([]byte, PACHeaderSize)

	if len(p.Common) > 0 {
		order.PutUint32(h[4:8], PACCommon)
	}
	order.PutUint64(h[24:32], uint64(len(p.Members)))
	copy(h[32:64], p.Digest[:])
	h = extended(h, uint64(len(p.Extensions)))
	seen := map[uint32]bool{}
	for _, e := range p.Extensions {
		if seen[e.Type] {
			return nil, fmt.Errorf("duplicate PAC extension")
		}
		seen[e.Type] = true
		if len(e.Data) > PACMaximum-len(h)-22 {
			return nil, fmt.Errorf("PAC extension exceeds limit")
		}
		h = order.AppendUint32(h, e.Type)
		h = order.AppendUint16(h, 0) // schema version zero
		var flags uint16
		if e.Required {
			flags = 1
		}
		h = order.AppendUint16(h, flags)
		h = extended(h, uint64(len(e.Data)))
		h = append(h, e.Data...)
	}
	h = extended(h, uint64(len(p.Common)))
	h = append(h, p.Common...)
	order.PutUint64(h[16:24], uint64(len(h)))
	total := len(h)
	for _, m := range p.Members {
		if len(m) > PACMaximum-total {
			return nil, fmt.Errorf("PAC exceeds byte limit")
		}
		total += len(m)
	}
	order.PutUint32(h[:4], uint32(total))
	order.PutUint64(h[8:16], uint64(total))
	out := make([]byte, 0, total)
	out = append(out, h...)
	for _, m := range p.Members {
		out = append(out, m...)
	}
	return out, nil
}

// ParsePAC borrows all frame/extension bytes. supported lists understood required
// extension types; unknown optional types remain available but can be skipped.
func ParsePAC(l *Layout, raw []byte, supported map[uint32]bool) (*PAC, error) {
	if len(raw) < PACHeaderSize || uint64(order.Uint32(raw[:4])) != uint64(len(raw)) {
		return nil, fmt.Errorf("invalid PAC stored length")
	}
	flags := order.Uint32(raw[4:8])
	size := order.Uint64(raw[8:16])
	head := order.Uint64(raw[16:24])
	count := order.Uint64(raw[24:32])
	if flags & ^PACCommon != 0 || size != uint64(len(raw)) || size > PACMaximum || head < PACHeaderSize || head > size {
		return nil, fmt.Errorf("invalid PAC bounds/features")
	}
	p := &PAC{Bytes: raw}
	copy(p.Digest[:], raw[32:64])
	if p.Digest != l.Digest {
		return nil, fmt.Errorf("PAC definition mismatch")
	}
	at := PACHeaderSize
	n, err := readExtended(raw[:head], &at)
	if err != nil {
		return nil, err
	}
	if n > uint64((int(head)-at)/10) {
		return nil, fmt.Errorf("invalid PAC extension count")
	}
	seen := map[uint32]bool{}
	for ; n > 0; n-- {
		if int(head)-at < 8 {
			return nil, io.ErrUnexpectedEOF
		}
		kind := order.Uint32(raw[at:])
		version := order.Uint16(raw[at+4:])
		ef := order.Uint16(raw[at+6:])
		at += 8
		length, e := readExtended(raw[:head], &at)
		if e != nil || length > uint64(int(head)-at) || seen[kind] || ef > 1 {
			return nil, fmt.Errorf("invalid PAC extension")
		}
		if ef == 1 && (version != 0 || !supported[kind]) {
			return nil, fmt.Errorf("unsupported required PAC extension %d", kind)
		}
		if supported[kind] && version != 0 {
			return nil, fmt.Errorf("unsupported PAC extension schema")
		}
		if kind == PACSource && version == 0 && !utf8.Valid(raw[at:at+int(length)]) {
			return nil, fmt.Errorf("invalid PAC source UTF-8")
		}
		seen[kind] = true
		p.Extensions = append(p.Extensions, PACExtension{kind, ef == 1, raw[at : at+int(length)]})
		at += int(length)
	}
	common, e := readExtended(raw[:head], &at)
	if e != nil || common != head-uint64(at) || (common > 0) != (flags&PACCommon != 0) {
		return nil, fmt.Errorf("invalid PAC common frame")
	}
	p.Common = raw[at:head]
	var prefix Frame
	if common > 0 {
		if e = l.scanPlain(p.Common, &prefix); e != nil {
			return nil, e
		}
	}
	mask := (len(l.Widths) + 3) / 4
	if mask == 0 || count > uint64((len(raw)-int(head))/mask) || count > math.MaxInt || count > PACMaximumMembers {
		return nil, fmt.Errorf("invalid PAC member count")
	}
	p.Members = make([][]byte, 0, int(count))
	r := bytes.NewReader(raw[head:])
	var scan Frame
	var scratch []byte
	for i := uint64(0); i < count; i++ {
		start := len(raw) - r.Len()
		if scratch, e = l.readFrame(r, scratch, false); e != nil {
			return nil, e
		}
		member := raw[start : len(raw)-r.Len()]
		if e = l.scanPlain(member, &scan); e != nil {
			return nil, e
		}
		for f, v := range prefix.Values {
			if v.State != Absent && scan.Values[f].State != Absent {
				return nil, fmt.Errorf("PAC common field overridden")
			}
		}
		p.Members = append(p.Members, member)
	}
	if r.Len() != 0 {
		return nil, fmt.Errorf("PAC trailing bytes")
	}
	return p, nil
}

// ResolvePAC is an explicit standalone export, not the presentation/storage path.
func ResolvePAC(l *Layout, common, member []byte) ([]byte, error) {
	var c, m Frame
	if err := l.scanPlain(member, &m); err != nil {
		return nil, err
	}
	if len(common) > 0 {
		if err := l.scanPlain(common, &c); err != nil {
			return nil, err
		}
		for i, v := range c.Values {
			if v.State != Absent {
				if m.Values[i].State != Absent {
					return nil, fmt.Errorf("PAC override")
				}
				m.Values[i] = v
			}
		}
	}
	return l.Encode(nil, m.Values)
}
