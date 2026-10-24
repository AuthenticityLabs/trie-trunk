package cache

// Test-only collection-output experiment, not a production PIL format.
// A package carries an explicit absolute/relative profile, record count and,
// for relative data, one signed millisecond base. Each frame retains the normal
// field mask. Relative UUIDv7 uses U32 offset + untouched trailing ten bytes;
// relative TIMESTAMP_MS uses U32 offset. Field selection is compiled once.
import (
	"encoding/binary"
	"fmt"
	"math"
)

type timebasePlan struct {
	layout     *Layout
	relative   []bool
	last, mask int
}

func compileTimebaseExperiment(l *Layout) *timebasePlan {
	p := &timebasePlan{layout: l, relative: make([]bool, len(l.Widths)), last: -1, mask: (len(l.Widths) + 3) / 4}
	for i, f := range l.Definition.Fields {
		// The event role's UUIDv7 restriction is an explicit experiment precondition.
		if i == l.Event || f.Type == "TIMESTAMP_MS" {
			p.relative[i] = true
			p.last = i
		}
	}
	return p
}
func uuidMillis(raw []byte) uint64 { return binary.BigEndian.Uint64(raw[:8]) >> 16 }
func (p *timebasePlan) transform(dst, raw []byte, base int64, expand bool) ([]byte, error) {
	if len(raw) < p.mask {
		return nil, fmt.Errorf("short mask")
	}
	at, start := p.mask, 0
	for i := 0; i <= p.last; i++ {
		state := State(raw[i/4] >> uint(6-2*(i%4)) & 3)
		if state != Direct {
			if state == Offset && p.relative[i] {
				return nil, fmt.Errorf("offset temporal field")
			}
			continue
		}
		w := p.layout.Widths[i]
		isUUID := i == p.layout.Event
		if p.relative[i] && expand {
			w = 4
			if isUUID {
				w = 14
			}
		}
		if w <= 0 || w > len(raw)-at {
			return nil, fmt.Errorf("short direct value")
		}
		if !p.relative[i] {
			at += w
			continue
		}
		dst = append(dst, raw[start:at]...)
		if expand {
			delta := int64(order.Uint32(raw[at:]))
			if base > math.MaxInt64-delta {
				return nil, fmt.Errorf("timestamp overflow")
			}
			ms := base + delta
			if isUUID {
				if ms < 0 || uint64(ms) >= 1<<48 || raw[at+4]>>4 != 7 || raw[at+6]&0xc0 != 0x80 {
					return nil, fmt.Errorf("invalid relative UUIDv7")
				}
				var stamp [8]byte
				binary.BigEndian.PutUint64(stamp[:], uint64(ms)<<16)
				dst = append(dst, stamp[:6]...)
				dst = append(dst, raw[at+4:at+14]...)
			} else {
				dst = order.AppendUint64(dst, uint64(ms))
			}
		} else {
			ms := int64(order.Uint64(raw[at:]))
			if isUUID {
				if raw[at+6]>>4 != 7 || raw[at+8]&0xc0 != 0x80 {
					return nil, fmt.Errorf("not UUIDv7")
				}
				ms = int64(uuidMillis(raw[at:]))
			}
			if ms < base || uint64(ms)-uint64(base) > math.MaxUint32 {
				return nil, fmt.Errorf("timestamp outside base range")
			}
			dst = order.AppendUint32(dst, uint32(uint64(ms)-uint64(base)))
			if isUUID {
				dst = append(dst, raw[at+6:at+16]...)
			}
		}
		at += w
		start = at
	}
	return append(dst, raw[start:]...), nil
}

type timebaseRow struct {
	absolute, relative []byte
	block              int
	ms                 int64
}
type timebaseBlock struct{ base int64 }
type timebaseOutput struct{ Bytes, Packets, Records int64 }

// Same 64-KiB packetization in every mode. PIL frames remain self-delimiting;
// there is no added per-record length or base identifier.
// Header: profile U32 + row count U32; relative profile adds base I64.
// Preserve source-block boundaries: a one-row hit pays the full base overhead.
func (p *timebasePlan) collect(rows []timebaseRow, blocks []timebaseBlock, selected []int, mode string, buffer []byte, send func([]byte) error) (timebaseOutput, error) {
	var out timebaseOutput
	buf := buffer[:0]
	block, count := -1, uint32(0)
	relativeOutput := mode == "relative_forward" || mode == "absolute_compact"
	flush := func() error {
		if count == 0 {
			return nil
		}
		order.PutUint32(buf[4:8], count)
		if err := send(buf); err != nil {
			return err
		}
		out.Bytes += int64(len(buf))
		out.Packets++
		out.Records += int64(count)
		buf = buf[:0]
		count = 0
		return nil
	}
	for _, number := range selected {
		row := rows[number]
		// Absolute frame length is an upper bound for all experimental outputs.
		if row.block != block || len(buf)+len(row.absolute) > 65536 {
			if err := flush(); err != nil {
				return out, err
			}
			block = row.block
		}
		if count == 0 {
			profile := uint32(0)
			if relativeOutput {
				profile = 1
			}
			buf = order.AppendUint32(buf, profile)
			buf = order.AppendUint32(buf, 0)
			if relativeOutput {
				buf = order.AppendUint64(buf, uint64(blocks[block].base))
			}
		}
		var err error
		switch mode {
		case "absolute_forward":
			buf = append(buf, row.absolute...)
		case "relative_forward":
			buf = append(buf, row.relative...)
		case "relative_expand":
			buf, err = p.transform(buf, row.relative, blocks[block].base, true)
		case "absolute_compact":
			buf, err = p.transform(buf, row.absolute, blocks[block].base, false)
		default:
			return out, fmt.Errorf("unknown mode")
		}
		if err != nil {
			return out, err
		}
		count++
	}
	err := flush()
	return out, err
}

// Receiver-side framing used for correctness checks, outside sender timing.
// Definition widths plus field states and final End Offset locate the frame.
func (p *timebasePlan) frameLength(raw []byte, relative bool) (int, error) {
	if len(raw) < p.mask {
		return 0, fmt.Errorf("short mask")
	}
	at := p.mask
	offsets := 0
	for i, w := range p.layout.Widths {
		state := State(raw[i/4] >> uint(6-2*(i%4)) & 3)
		if state == Direct {
			if relative && p.relative[i] {
				w = 4
				if i == p.layout.Event {
					w = 14
				}
			}
			if w <= 0 || w > len(raw)-at {
				return 0, fmt.Errorf("short direct field")
			}
			at += w
		} else if state == Offset {
			offsets++
		}
	}
	var end uint64
	for i := 0; i < offsets; i++ {
		next, err := readExtended(raw, &at)
		if err != nil || next < end {
			return 0, fmt.Errorf("invalid end offset")
		}
		end = next
	}
	if end > uint64(len(raw)-at) {
		return 0, fmt.Errorf("short variable section")
	}
	return at + int(end), nil
}
