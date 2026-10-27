package cache

import (
	"fmt"
	pil "github.com/AuthenticityLabs/pill-packer/go/codec"
	"math"
)

// One fixed stride per numeric column. Daily chunks share these descriptors;
// provisional standalone hours carry their own unbased descriptors.
const summaryNumericColumns = 9
const summaryEncodingSize = summaryNumericColumns * 9
const summaryRollupSize = 6 * 8

type summaryNumber = pil.UnsignedEncoding
type summaryEncoding [summaryNumericColumns]summaryNumber

func summaryValueWidth(v uint64) int                         { return pil.UnsignedWidth(v) }
func summaryPut(raw []byte, v uint64, c summaryNumber)       { pil.PutUnsigned(raw, v, c) }
func summaryGet(raw []byte, c summaryNumber) (uint64, error) { return pil.GetUnsigned(raw, c) }

func (e summaryEncoding) put(raw []byte) {
	for i, c := range e {
		raw[i*9] = byte(c.Width)
		order.PutUint64(raw[i*9+1:], c.Base)
	}
}
func readSummaryEncoding(raw []byte) (e summaryEncoding, err error) {
	for i := range e {
		e[i] = summaryNumber{int(raw[i*9]), order.Uint64(raw[i*9+1:])}
		if e[i].Width < 1 || e[i].Width > 8 || (i != 3 && i != 4 && i != 7) && e[i].Base != 0 {
			return e, fmt.Errorf("invalid summary column encoding")
		}
	}
	if e[2].Width != 8 {
		return e, fmt.Errorf("summary sum must be U64")
	}
	return
}
func summaryNumbers(s Summary, visit func(int, uint64)) {
	for k, c := range [][]uint64{s.Columns.Count, s.Columns.DurationCount, s.Columns.Sum, s.Columns.Min, s.Columns.Max} {
		for i, v := range c {
			// No duration sample has no minimum/maximum. Its stored residual is zero.
			if (k == 3 || k == 4) && s.Columns.DurationCount[i] == 0 {
				continue
			}
			visit(k, v)
		}
	}
	for _, g := range s.Statistics {
		visit(5, g.Count)
		visit(6, g.Errors)
		for _, p := range g.Durations {
			visit(7, p[0])
			visit(8, p[1])
		}
	}
}
func chooseSummaryEncoding(sums []Summary, based bool) (summaryEncoding, error) {
	var e summaryEncoding
	var maxima [summaryNumericColumns]uint64
	base := uint64(math.MaxUint64)
	seen := false
	for _, s := range sums {
		if _, err := validateSummary(s); err != nil {
			return e, err
		}
		summaryNumbers(s, func(k int, v uint64) {
			maxima[k] = max(maxima[k], v)
			if k == 3 || k == 4 || k == 7 {
				base = min(base, v)
				seen = true
			}
		})
	}
	if !based || !seen {
		base = 0
	}
	for k, v := range maxima {
		e[k].Width = summaryValueWidth(v)
		if k == 2 {
			e[k].Width = 8
		}
		if k == 3 || k == 4 || k == 7 {
			// Histogram may be absent even when aggregate duration values exist.
			if v >= base {
				e[k] = summaryNumber{summaryValueWidth(v - base), base}
			}
		}
	}
	// Counts cover an entire hourly population, including all events in a single
	// bucket. No divide-by-twelve assumption or U24 truncation; widen when needed.
	return e, nil
}
func (e summaryEncoding) fits(s Summary) error {
	var err error
	summaryNumbers(s, func(k int, v uint64) {
		c := e[k]
		if v < c.Base || summaryValueWidth(v-c.Base) > c.Width {
			err = fmt.Errorf("summary value exceeds column width")
		}
	})
	return err
}
func summaryTotals(sums []Summary) (a Aggregate, errors uint64, err error) {
	for _, s := range sums {
		for i := 0; i < s.Columns.Len(); i++ {
			if err = a.merge(s.Columns.At(i).Aggregate); err != nil {
				return
			}
		}
		for _, g := range s.Statistics {
			if g.Errors > math.MaxUint64-errors {
				err = fmt.Errorf("daily error count overflow")
				return
			}
			errors += g.Errors
		}
	}
	return
}
func putSummaryTotals(raw []byte, a Aggregate, errors uint64) {
	for i, v := range []uint64{a.Count, a.DurationCount, a.Sum, a.Min, a.Max, errors} {
		order.PutUint64(raw[i*8:], v)
	}
}

// Byte shuffle keeps each numeric column's equal-significance bytes together
// for gzip. It is reversed once on packet decode, before exposing fixed-stride
// columns. It does not reorder rows or introduce predecessor dependencies.
func shuffleSummary(raw []byte, enc summaryEncoding, reverse bool) error {
	if len(raw) < 116 {
		return fmt.Errorf("short summary columns")
	}
	n, m, p := int(order.Uint32(raw[32:])), int(order.Uint32(raw[36:])), int(order.Uint32(raw[40:]))
	at := 116 + n*2
	spans := make([]pil.ColumnSpan, 0, summaryNumericColumns)
	column := func(count, width int) {
		spans = append(spans, pil.ColumnSpan{Offset: at, Count: count, Width: width})
		at += count * width
	}
	for k := 0; k < 5; k++ {
		column(n, enc[k].Width)
	}
	at += m * 5
	column(m, enc[5].Width)
	column(m, enc[6].Width)
	at += m * 8
	column(p, enc[7].Width)
	column(p, enc[8].Width)
	return pil.ShuffleColumns(raw, spans, reverse)
}

func validateShuffledSummary(raw []byte, enc summaryEncoding) error {
	if len(raw) < 116 || string(raw[:8]) != "SUMCOL00" || raw[48] != 1 {
		return fmt.Errorf("invalid shuffled summary")
	}
	n, m, p, d := uint64(order.Uint32(raw[32:])), uint64(order.Uint32(raw[36:])), uint64(order.Uint32(raw[40:])), uint64(order.Uint32(raw[44:]))
	size := uint64(116) + n*uint64(2+enc[0].Width+enc[1].Width+enc[2].Width+enc[3].Width+enc[4].Width) + m*uint64(13+enc[5].Width+enc[6].Width) + p*uint64(enc[7].Width+enc[8].Width) + d
	if size != uint64(len(raw)) {
		return fmt.Errorf("invalid shuffled column lengths")
	}
	return nil
}
