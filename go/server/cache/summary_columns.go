package cache

// Columnar five-minute aggregates. Field values are never retained here: only
// aggregate state and the exact duration histogram required by the current UI.
import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
)

const sumColumnHeader = 64
const sumBucketMS int64 = 300000

func validateSummary(s Summary) (int, error) {
	c := s.Columns
	if s.DurationWidth != 0 && s.DurationWidth != 1 && s.DurationWidth != 2 && s.DurationWidth != 3 && s.DurationWidth != 4 && s.DurationWidth != 8 {
		return 0, fmt.Errorf("invalid duration Definition width")
	}

	n := c.Len()
	if n == 0 || s.Through <= s.From || len(c.Period) != n || len(c.Reference) != n || len(c.DurationCount) != n || len(c.Sum) != n || len(c.Min) != n || len(c.Max) != n {
		return 0, fmt.Errorf("invalid summary columns")
	}
	var typeErr error
	if s.DurationWidth > 0 {
		summaryNumbers(s, func(k int, v uint64) {
			if (k == 3 || k == 4 || k == 7) && s.DurationWidth < 8 && v >= uint64(1)<<(8*s.DurationWidth) {
				typeErr = fmt.Errorf("duration exceeds Definition width")
			}
		})
	}
	if typeErr != nil {
		return 0, typeErr
	}
	hour := utcHour(c.Period[0])
	buckets := 0
	var count, durations uint64
	for i := 0; i < n; i++ {
		g := c.At(i)
		if g.Period < 0 || g.Period%sumBucketMS != 0 || utcHour(g.Period) != hour || !validAggregate(g.Aggregate) || g.Aggregate.Count > math.MaxUint64-count {
			return 0, fmt.Errorf("invalid summary aggregate")
		}
		if i == 0 || g.Period != c.Period[i-1] {
			if i > 0 && g.Period < c.Period[i-1] {
				return 0, fmt.Errorf("unordered buckets")
			}
			buckets++
		} else if g.Reference <= c.Reference[i-1] {
			return 0, fmt.Errorf("unordered references")
		}
		count += g.Aggregate.Count
		durations += g.Aggregate.DurationCount
	}
	if count != s.Through-s.From {
		return 0, fmt.Errorf("summary source mismatch")
	}
	if len(s.Statistics) > 0 {
		type key struct {
			period int64
			labels [4]string
		}
		seen := map[key]bool{}
		var total, samples uint64
		for _, g := range s.Statistics {
			k := key{g.Period, g.Labels}
			if g.Period%sumBucketMS != 0 || utcHour(g.Period) != hour || g.Count == 0 || total > count || g.Count > count-total || g.Errors > g.Count || seen[k] {
				return 0, fmt.Errorf("invalid statistics group")
			}
			seen[k] = true
			total += g.Count
			var part uint64
			for i, p := range g.Durations {
				if p[1] == 0 || part > g.Count || p[1] > g.Count-part || i > 0 && p[0] <= g.Durations[i-1][0] {
					return 0, fmt.Errorf("invalid duration distribution")
				}
				part += p[1]
			}
			samples += part
		}
		if total != count || samples != durations {
			return 0, fmt.Errorf("statistics source mismatch")
		}
	}
	return buckets, nil
}

// Header retains source watermarks and bucket dimensions. Byte 48 selects
// local (0) or daily (1) column descriptors; byte 49 is the Definition's duration
// width. Daily descriptors are stored once, outside the hourly chunks.
func encodeSummary(s Summary) ([]byte, error) { return encodeSummaryColumns(s, nil) }
func encodeSummaryColumns(s Summary, shared *summaryEncoding) ([]byte, error) {
	if _, e := validateSummary(s); e != nil {
		return nil, e
	}
	enc := shared
	if enc == nil {
		e, err := chooseSummaryEncoding([]Summary{s}, false)
		if err != nil {
			return nil, err
		}
		enc = &e
	}
	if e := enc.fits(s); e != nil {
		return nil, e
	}
	labels := [][4]string{}
	ids := map[[4]string]uint32{}
	var pairs int
	for _, g := range s.Statistics {
		if _, ok := ids[g.Labels]; !ok {
			ids[g.Labels] = uint32(len(labels))
			labels = append(labels, g.Labels)
		}
		pairs += len(g.Durations)
	}
	dict, e := json.Marshal(labels)
	if e != nil {
		return nil, e
	}
	n, m := s.Columns.Len(), len(s.Statistics)
	columnsStart := sumColumnHeader + 52
	if shared == nil {
		columnsStart += summaryEncodingSize
	}
	size := columnsStart + n*(2+enc[0].Width+enc[1].Width+enc[2].Width+enc[3].Width+enc[4].Width) + m*(13+enc[5].Width+enc[6].Width) + pairs*(enc[7].Width+enc[8].Width) + len(dict)
	if size > MaxFrame {
		return nil, fmt.Errorf("summary too large")
	}
	raw := make([]byte, size)
	copy(raw, "SUMCOL00")
	order.PutUint64(raw[8:], s.From)
	order.PutUint64(raw[16:], s.Through)
	hour := utcHour(s.Columns.Period[0])
	order.PutUint64(raw[24:], uint64(hour))
	order.PutUint32(raw[32:], uint32(n))
	order.PutUint32(raw[36:], uint32(m))
	order.PutUint32(raw[40:], uint32(pairs))
	order.PutUint32(raw[44:], uint32(len(dict)))
	if shared != nil {
		raw[48] = 1
	} else {
		enc.put(raw[116:])
	}
	raw[49] = s.DurationWidth
	row := 0
	for bucket := 0; bucket <= 12; bucket++ {
		for row < n && s.Columns.Period[row] < hour+int64(bucket)*sumBucketMS {
			row++
		}
		order.PutUint32(raw[64+bucket*4:], uint32(row))
	}
	at := columnsStart
	for _, r := range s.Columns.Reference {
		order.PutUint16(raw[at:], r)
		at += 2
	}
	for k, col := range [][]uint64{s.Columns.Count, s.Columns.DurationCount, s.Columns.Sum, s.Columns.Min, s.Columns.Max} {
		for i, v := range col {
			if (k == 3 || k == 4) && s.Columns.DurationCount[i] == 0 {
				v = enc[k].Base
			}
			summaryPut(raw[at:], v, enc[k])
			at += enc[k].Width
		}
	}
	for _, g := range s.Statistics {
		raw[at] = byte((g.Period - hour) / sumBucketMS)
		at++
	}
	for _, g := range s.Statistics {
		order.PutUint32(raw[at:], ids[g.Labels])
		at += 4
	}
	for k, errors := range []bool{false, true} {
		for _, g := range s.Statistics {
			v := g.Count
			if errors {
				v = g.Errors
			}
			summaryPut(raw[at:], v, enc[5+k])
			at += enc[5+k].Width
		}
	}
	for _, end := range []bool{false, true} {
		p := 0
		for _, g := range s.Statistics {
			v := p
			if end {
				v += len(g.Durations)
			}
			order.PutUint32(raw[at:], uint32(v))
			at += 4
			p += len(g.Durations)
		}
	}
	for k := 0; k < 2; k++ {
		for _, g := range s.Statistics {
			for _, pair := range g.Durations {
				summaryPut(raw[at:], pair[k], enc[7+k])
				at += enc[7+k].Width
			}
		}
	}
	copy(raw[at:], dict)
	return raw, nil
}
func decodeSummary(raw []byte, s *Summary) error { return decodeSummaryColumns(raw, s, nil) }
func decodeSummaryColumns(raw []byte, s *Summary, shared *summaryEncoding) error {
	if len(raw) < 116 || len(raw) > MaxFrame || string(raw[:8]) != "SUMCOL00" || !bytes.Equal(raw[50:64], make([]byte, 14)) || raw[48] > 1 || (raw[48] == 1) != (shared != nil) {
		return fmt.Errorf("invalid columnar summary")
	}
	n, m, p, d := uint64(order.Uint32(raw[32:])), uint64(order.Uint32(raw[36:])), uint64(order.Uint32(raw[40:])), uint64(order.Uint32(raw[44:]))
	enc := shared
	columnsStart := 116
	if enc == nil {
		if len(raw) < 116+summaryEncodingSize {
			return fmt.Errorf("short column descriptors")
		}
		e, err := readSummaryEncoding(raw[116:])
		if err != nil {
			return err
		}
		enc = &e
		columnsStart += summaryEncodingSize
	}
	expected := uint64(columnsStart) + n*uint64(2+enc[0].Width+enc[1].Width+enc[2].Width+enc[3].Width+enc[4].Width) + m*uint64(13+enc[5].Width+enc[6].Width) + p*uint64(enc[7].Width+enc[8].Width) + d
	if n == 0 || expected != uint64(len(raw)) {
		return fmt.Errorf("invalid column lengths")
	}
	hour := int64(order.Uint64(raw[24:]))
	if hour < 0 || hour%TimeHourMS != 0 {
		return fmt.Errorf("invalid summary hour")
	}
	out := Summary{DurationWidth: raw[49], From: order.Uint64(raw[8:]), Through: order.Uint64(raw[16:])}
	c := &out.Columns
	c.Period = make([]int64, n)
	c.Reference = make([]uint16, n)
	prior := uint32(0)
	if order.Uint32(raw[64:]) != 0 {
		return fmt.Errorf("invalid bucket start")
	}
	for bucket := 0; bucket < 12; bucket++ {
		end := order.Uint32(raw[68+bucket*4:])
		if end < prior || uint64(end) > n {
			return fmt.Errorf("invalid bucket boundary")
		}
		for i := prior; i < end; i++ {
			c.Period[i] = hour + int64(bucket)*sumBucketMS
		}
		prior = end
	}
	if uint64(prior) != n {
		return fmt.Errorf("invalid bucket tail")
	}
	at := columnsStart
	for i := range c.Reference {
		c.Reference[i] = order.Uint16(raw[at:])
		at += 2
	}
	for k, col := range []*[]uint64{&c.Count, &c.DurationCount, &c.Sum, &c.Min, &c.Max} {
		*col = make([]uint64, n)
		for i := range *col {
			v, err := summaryGet(raw[at:], enc[k])
			if err != nil {
				return err
			}
			at += enc[k].Width
			if (k == 3 || k == 4) && c.DurationCount[i] == 0 {
				if v != enc[k].Base {
					return fmt.Errorf("nonzero absent duration")
				}
				v = 0
			}
			(*col)[i] = v
		}
	}
	var labels [][4]string
	if e := json.Unmarshal(raw[len(raw)-int(d):], &labels); e != nil {
		return e
	}
	if m > 0 {
		out.Statistics = make([]statisticsGroup, m)
	}
	for i := range out.Statistics {
		bucket := raw[at]
		at++
		if bucket >= 12 {
			return fmt.Errorf("invalid statistics bucket")
		}
		out.Statistics[i].Period = hour + int64(bucket)*sumBucketMS
	}
	for i := range out.Statistics {
		id := order.Uint32(raw[at:])
		at += 4
		if uint64(id) >= uint64(len(labels)) {
			return fmt.Errorf("invalid label ID")
		}
		out.Statistics[i].Labels = labels[id]
	}
	for k, errors := range []bool{false, true} {
		for i := range out.Statistics {
			v, err := summaryGet(raw[at:], enc[5+k])
			if err != nil {
				return err
			}
			at += enc[5+k].Width
			if errors {
				out.Statistics[i].Errors = v
			} else {
				out.Statistics[i].Count = v
			}
		}
	}
	starts := raw[at : at+int(m)*4]
	at += int(m) * 4
	ends := raw[at : at+int(m)*4]
	at += int(m) * 4
	values := raw[at : at+int(p)*enc[7].Width]
	at += int(p) * enc[7].Width
	counts := raw[at : at+int(p)*enc[8].Width]
	var previous uint32
	for i := range out.Statistics {
		a, z := order.Uint32(starts[i*4:]), order.Uint32(ends[i*4:])
		if a != previous || z < a || uint64(z) > p {
			return fmt.Errorf("invalid histogram bounds")
		}
		previous = z
		for j := a; j < z; j++ {
			v, err := summaryGet(values[int(j)*enc[7].Width:], enc[7])
			if err != nil {
				return err
			}
			count, err := summaryGet(counts[int(j)*enc[8].Width:], enc[8])
			if err != nil {
				return err
			}
			out.Statistics[i].Durations = append(out.Statistics[i].Durations, [2]uint64{v, count})
		}
	}
	if uint64(previous) != p {
		return fmt.Errorf("invalid histogram tail")
	}
	if _, e := validateSummary(out); e != nil {
		return e
	}
	*s = out
	return nil
}
