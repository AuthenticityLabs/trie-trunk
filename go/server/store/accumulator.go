package store

import (
	"fmt"
	"math"
	"sort"
	"time"
)

type Aggregate struct{ Count, Sum, Min, Max uint64 }

func (a *Aggregate) add(d uint64) error {
	if a.Count == math.MaxUint64 || a.Sum > math.MaxUint64-d {
		return ErrLimit
	}
	if a.Count == 0 || d < a.Min {
		a.Min = d
	}
	if d > a.Max {
		a.Max = d
	}
	a.Count++
	a.Sum += d
	return nil
}

type group struct {
	Bucket uint16
	Leaf   uint32
}

type Summary struct {
	Bucket  uint16
	Through int64
	Groups  map[uint32]Aggregate
}

func decodeSummary(b []byte) (Summary, error) { return decodeSummaryBuckets(b, 288) }

func (s *schema) decodeSummary(b []byte) (Summary, error) {
	if s.modern() {
		return decodeSummaryBuckets(b, 512)
	}
	return decodeSummary(b)
}

func decodeSummaryBuckets(b []byte, buckets uint16) (Summary, error) {
	var s Summary
	if len(b) < 14 {
		return s, fmt.Errorf("invalid summary")
	}
	s.Bucket = order.Uint16(b[:2])
	s.Through = int64(order.Uint64(b[2:10]))
	n := int(order.Uint32(b[10:14]))
	if s.Bucket >= buckets || n > (maxBlockSize-14)/36 || len(b) != 14+n*36 {
		return s, fmt.Errorf("invalid summary length")
	}
	s.Groups = make(map[uint32]Aggregate, n)
	for p := 14; p < len(b); p += 36 {
		leaf := order.Uint32(b[p:])
		if _, ok := s.Groups[leaf]; ok {
			return s, fmt.Errorf("duplicate aggregate group")
		}
		a := Aggregate{order.Uint64(b[p+4:]), order.Uint64(b[p+12:]), order.Uint64(b[p+20:]), order.Uint64(b[p+28:])}
		if a.Count == 0 || a.Min > a.Max {
			return s, fmt.Errorf("invalid aggregate")
		}
		s.Groups[leaf] = a
	}
	return s, nil
}

func (p *partition) finalize(now time.Time) error {
	cutoff := now.UTC().Truncate(5 * time.Minute).Add(-10 * time.Minute)
	if p.schema.modern() {
		cutoff = now.Add(-matureDelay - 5*time.Minute)
	}
	var buckets [512][]uint32
	for g := range p.acc {
		if !p.day.Add(time.Duration(g.Bucket) * 5 * time.Minute).After(cutoff) {
			buckets[g.Bucket] = append(buckets[g.Bucket], g.Leaf)
		}
	}
	written := false
	for bucketIndex, leaves := range buckets {
		bucket := uint16(bucketIndex)
		if len(leaves) == 0 {
			continue
		}
		sort.Slice(leaves, func(i, j int) bool { return leaves[i] < leaves[j] })
		b := make([]byte, 14+36*len(leaves))
		order.PutUint16(b[:2], bucket)
		order.PutUint64(b[2:10], uint64(p.watermark()))
		order.PutUint32(b[10:14], uint32(len(leaves)))
		for i, leaf := range leaves {
			a := p.acc[group{bucket, leaf}]
			off := 14 + i*36
			order.PutUint32(b[off:], leaf)
			order.PutUint64(b[off+4:], a.Count)
			order.PutUint64(b[off+12:], a.Sum)
			order.PutUint64(b[off+20:], a.Min)
			order.PutUint64(b[off+28:], a.Max)
		}
		if _, e := appendBlock(p.sum, b); e != nil {
			return e
		}
		written = true
	}
	if written {
		if e := p.sum.Sync(); e != nil {
			return e
		}
	}
	for i, leaves := range buckets {
		if len(leaves) == 0 {
			continue
		}
		bucket := uint16(i)
		p.watermarks[bucket] = p.watermark()
		for _, leaf := range leaves {
			delete(p.acc, group{bucket, leaf})
		}
	}
	return nil
}
