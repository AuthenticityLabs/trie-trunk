package store

import (
	"bytes"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"time"

	"trie-trunk/go/codec"
)

const indexRowSize = 36

type row struct {
	Id         codec.UUIDv7
	Leaf       uint32
	Dynamic    []byte
	Start, End uint64
}

type run struct {
	Offset, End int64
	Count       uint32
	Min, Max    codec.UUIDv7
}

type partition struct {
	day           time.Time
	scope         scope
	compact       bool
	dir           string
	schema        *schema
	idx, pop, sum *os.File
	trie          *trie
	popEnd        int64
	runs          []run
	pending       []row
	committedMax  codec.UUIDv7
	pendingBytes  int
	acc           map[group]Aggregate
	watermarks    [512]int64
	indexEnd      int64
	config        Config
	touched       uint64
}

func openPartition(root string, s *schema, day time.Time, c Config) (p *partition, err error) {
	return openScopedPartition(root, s, scope{Start: day, End: day.AddDate(0, 0, 1), Dir: dayPath(day)}, c)
}
func openScopedPartition(root string, s *schema, sc scope, c Config) (p *partition, err error) {
	dir := filepath.Join(s.directory(root, c.Tap), sc.Dir)
	day := s.day(sc.Start)
	if err = mkdirDurable(dir); err != nil {
		return nil, err
	}
	p = &partition{scope: sc, dir: dir, day: day, schema: s, config: c, popEnd: fileHeaderSize, acc: make(map[group]Aggregate), indexEnd: fileHeaderSize}
	defer func(opened *partition) {
		if err != nil {
			opened.closeFiles()
		}
	}(p)
	if p.trie, err = acquireTrie(c, s); err != nil {
		return nil, err
	}
	if p.idx, err = p.openIndex(); err != nil {
		return nil, err
	}
	if p.pop, err = s.openLog(filepath.Join(dir, s.filename("pop")), "TPOP"); err != nil {
		return nil, err
	}
	if p.sum, err = s.openLog(filepath.Join(dir, s.filename("sum")), "TSUM"); err != nil {
		return nil, err
	}
	if err = syncDirectory(dir); err != nil {
		return nil, err
	}
	if err = scan(p.sum, func(_, _ int64, b []byte) error {
		v, e := s.decodeSummary(b)
		if e != nil {
			return e
		}
		if v.Through <= p.watermarks[v.Bucket] || v.Through < fileHeaderSize {
			return fmt.Errorf("invalid summary watermark")
		}
		for leaf := range v.Groups {
			if int(leaf) >= len(p.trie.nodes) {
				return fmt.Errorf("invalid summary leaf")
			}
		}
		p.watermarks[v.Bucket] = v.Through
		return nil
	}); err != nil {
		return nil, err
	}
	if err = scan(p.idx, func(start, end int64, b []byte) error {
		if s.modern() && start == fileHeaderSize {
			return p.checkScope(b)
		}
		rows, e := p.decodeRun(b)
		if e != nil {
			return e
		}
		if len(p.runs) >= c.MaxRuns {
			return ErrLimit
		}
		if rows[0].Start != uint64(p.popEnd+8) {
			return fmt.Errorf("noncontiguous population block")
		}
		pop, e := p.readPopRun(rows)
		if e != nil {
			return e
		}
		for _, r := range rows {
			if int(r.Leaf) >= len(p.trie.nodes) || !p.accepts(r) {
				return fmt.Errorf("invalid index reference/day")
			}
			fields, e := codec.Decode(pop[r.Start-rows[0].Start:r.End-rows[0].Start], codec.CBOR)
			if e != nil {
				return fmt.Errorf("invalid population CBOR: %w", e)
			}
			duration, ok := fields[s.duration].(uint64)
			if s.hasDuration && (!ok || duration > uint64((7*24*time.Hour)/time.Microsecond)) {
				return fmt.Errorf("invalid population duration")
			}
			bucket := s.bucket(r.Id)
			if int(bucket) >= len(p.watermarks) {
				return fmt.Errorf("civil day exceeds summary bucket capacity")
			}
			through := end
			if s.modern() {
				through = int64(rows[len(rows)-1].End)
			}
			if through > p.watermarks[bucket] {
				if e = p.addAggregate(bucket, r.Leaf, duration); e != nil {
					return e
				}
			}
		}
		s.observe(sc.Dir, rows, c.Now())
		p.runs = append(p.runs, run{start, end, uint32(len(rows)), rows[0].Id, rows[len(rows)-1].Id})
		if last := rows[len(rows)-1].Id; bytes.Compare(last[:], p.committedMax[:]) > 0 {
			p.committedMax = last
		}
		p.indexEnd = end
		p.popEnd = int64(rows[len(rows)-1].End)
		return nil
	}); err != nil {
		return nil, err
	}
	for _, v := range p.watermarks {
		if v > p.watermark() {
			return nil, fmt.Errorf("summary references uncommitted index")
		}
	}

	if err = p.pop.Truncate(p.popEnd); err != nil {
		return nil, err
	}
	if err = errors.Join(p.pop.Sync(), p.idx.Sync(), p.sum.Sync()); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *partition) closeFiles() error {
	var errs []error
	for _, f := range []*os.File{p.idx, p.pop, p.sum} {
		if f != nil {
			errs = append(errs, f.Close())
		}
	}
	if p.trie != nil {
		errs = append(errs, p.trie.release())
		p.trie = nil
	}
	return errors.Join(errs...)
}

func (p *partition) addAggregate(bucket uint16, leaf uint32, d uint64) error {
	if !p.schema.aggregates {
		return nil
	}
	if int(bucket) >= len(p.watermarks) {
		return ErrLimit
	}
	g := group{bucket, leaf}
	a, ok := p.acc[g]
	if !ok && len(p.acc) >= p.config.MaxGroups {
		return ErrLimit
	}
	if e := a.add(d); e != nil {
		return e
	}
	p.acc[g] = a
	return nil
}

func (p *partition) contains(id codec.UUIDv7) (bool, error) {
	if len(p.runs) == 0 || bytes.Compare(id[:], p.committedMax[:]) > 0 {
		return false, nil
	}

	rowSize, runHeader := p.rowLayout()
	for runIndex := len(p.runs) - 1; runIndex >= 0; runIndex-- {
		r := p.runs[runIndex]
		if bytes.Compare(id[:], r.Min[:]) < 0 || bytes.Compare(id[:], r.Max[:]) > 0 {
			continue
		}
		lo, hi := uint32(0), r.Count

		tailCount := min(uint32(8), r.Count)
		tailStart := r.Count - tailCount
		var tail [8 * indexRowSize]byte
		if _, e := p.idx.ReadAt(tail[:tailCount*uint32(rowSize)], r.Offset+8+int64(runHeader)+int64(tailStart)*int64(rowSize)); e != nil {
			return false, e
		}
		tailResolved := false
		for i := int(tailCount) - 1; i >= 0; i-- {
			cmp := bytes.Compare(tail[i*rowSize:i*rowSize+16], id[:])
			if cmp == 0 {
				return true, nil
			}
			if cmp < 0 {
				tailResolved = true
				break
			}
		}
		if tailResolved {
			continue
		}
		hi = tailStart
		for lo < hi {
			mid := lo + (hi-lo)/2
			var got codec.UUIDv7
			if _, e := p.idx.ReadAt(got[:], r.Offset+8+int64(runHeader)+int64(mid)*int64(rowSize)); e != nil {
				return false, e
			}
			cmp := bytes.Compare(got[:], id[:])
			if cmp == 0 {
				return true, nil
			}
			if cmp < 0 {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
	}
	return false, nil
}

func (p *partition) append(r record, now time.Time) (bool, error) {
	position, duplicate := p.insertionPoint(r.Id)
	if duplicate {
		return true, nil
	}
	duplicate, e := p.contains(r.Id)
	if e != nil || duplicate {
		return duplicate, e
	}
	if len(p.pending) >= p.config.BatchRecords || p.pendingBytes+len(r.Dynamic)+indexRowSize > p.config.BatchBytes {
		if e = p.flush(now); e != nil {
			return false, e
		}
		position = 0
	}
	if len(p.runs) >= p.config.MaxRuns {
		return false, ErrLimit
	}
	if !r.Prepared {
		r, e = p.trie.prepare(r, p.config)
		if e != nil {
			return false, e
		}
	}
	parent := r.Leaf
	if p.schema.narrowReferences() && parent > 65535 {
		return false, fmt.Errorf("trie reference exceeds U16")
	}

	if len(r.Dynamic) > codec.MaxFramePayload {
		return false, ErrLimit
	}
	if len(p.pending) > 0 && p.pendingBytes+len(r.Dynamic)+indexRowSize > p.config.BatchBytes {
		if e = p.flush(now); e != nil {
			return false, e
		}
		position = 0
		if len(p.runs) >= p.config.MaxRuns {
			return false, ErrLimit
		}
	}
	if e = p.addAggregate(p.schema.bucket(r.Id), parent, r.Duration); e != nil {
		return false, e
	}
	p.pending = append(p.pending, row{})
	copy(p.pending[position+1:], p.pending[position:len(p.pending)-1])
	p.pending[position] = row{Id: r.Id, Leaf: parent, Dynamic: r.Dynamic}
	p.pendingBytes += indexRowSize + len(r.Dynamic)
	return false, nil
}

func (p *partition) flush(now time.Time) error {
	if p.config.recoverOnly {
		return nil
	}
	if len(p.pending) > 0 {
		start := p.indexEnd
		end, e := p.commitPending()
		if e != nil {
			return e
		}
		p.runs = append(p.runs, run{start, end, uint32(len(p.pending)), p.pending[0].Id, p.pending[len(p.pending)-1].Id})
		if last := p.pending[len(p.pending)-1].Id; bytes.Compare(last[:], p.committedMax[:]) > 0 {
			p.committedMax = last
		}
		p.indexEnd = end
		p.schema.observe(p.scope.Dir, p.pending, now)
		clear(p.pending)
		p.pending = p.pending[:0]
		p.pendingBytes = 0
	}
	if e := p.finalize(now); e != nil {
		return e
	}
	return p.mature(now)
}

func (p *partition) commitPending() (int64, error) {
	payload := make([]byte, 0, p.pendingBytes)
	for i := range p.pending {
		r := &p.pending[i]
		r.Start = uint64(p.popEnd + 8 + int64(len(payload)))
		payload = append(payload, r.Dynamic...)
		r.End = uint64(p.popEnd + 8 + int64(len(payload)))
	}
	if err := p.trie.file.Sync(); err != nil {
		return 0, err
	}
	end, err := appendBlock(p.pop, payload)
	if err != nil {
		return 0, err
	}
	if err = p.pop.Sync(); err != nil {
		return 0, err
	}
	p.popEnd = end
	encoded, err := p.encodeRun(p.pending)
	if err != nil {
		return 0, err
	}
	end, err = appendBlock(p.idx, encoded)
	if err != nil {
		return 0, err
	}
	return end, p.idx.Sync()
}

func encodeRun(rows []row) []byte {
	b := make([]byte, 4+indexRowSize*len(rows))
	order.PutUint32(b[:4], uint32(len(rows)))
	for i, r := range rows {
		pos := 4 + i*indexRowSize
		copy(b[pos:pos+16], r.Id[:])
		order.PutUint32(b[pos+16:pos+20], r.Leaf)
		order.PutUint64(b[pos+20:pos+28], r.Start)
		order.PutUint64(b[pos+28:pos+36], r.End)
	}
	return b
}

func decodeRun(b []byte) ([]row, error) {
	if len(b) < 4 {
		return nil, fmt.Errorf("short index block")
	}
	n := int(order.Uint32(b[:4]))
	if n == 0 || n > (len(b)-4)/indexRowSize || len(b) != 4+n*indexRowSize {
		return nil, fmt.Errorf("invalid run length")
	}
	rows := make([]row, n)
	for i := range rows {
		pos := 4 + i*indexRowSize
		r, err := decodeRow(b[pos : pos+indexRowSize])
		if err != nil {
			return nil, err
		}
		rows[i] = r
		if i > 0 && (rows[i-1].End != r.Start || bytes.Compare(rows[i-1].Id[:], r.Id[:]) >= 0) {
			return nil, fmt.Errorf("unordered index run")
		}
	}
	return rows, nil
}

func decodeRow(b []byte) (row, error) {
	var r row
	if len(b) != indexRowSize {
		return r, fmt.Errorf("invalid index row length")
	}
	copy(r.Id[:], b[:16])
	r.Leaf = order.Uint32(b[16:20])
	r.Start = order.Uint64(b[20:28])
	r.End = order.Uint64(b[28:36])
	if !r.Id.Valid() || r.Start < fileHeaderSize+8 || r.End <= r.Start || r.End-r.Start > codec.MaxFramePayload || r.End > uint64(^uint64(0)>>1) {
		return row{}, fmt.Errorf("invalid index range")
	}
	return r, nil
}

func (p *partition) readPopRun(rows []row) ([]byte, error) {
	start, end := rows[0].Start, rows[len(rows)-1].End
	if end-start > maxBlockSize {
		return nil, fmt.Errorf("oversized population block")
	}
	var header [8]byte
	if _, err := p.pop.ReadAt(header[:], int64(start)-8); err != nil {
		return nil, err
	}
	if uint64(order.Uint32(header[:4])) != end-start {
		return nil, fmt.Errorf("population length mismatch")
	}
	data := make([]byte, int(end-start))
	if _, err := p.pop.ReadAt(data, int64(start)); err != nil {
		return nil, err
	}
	if crc32.Checksum(data, crcTable) != order.Uint32(header[4:]) {
		return nil, fmt.Errorf("population checksum mismatch")
	}
	return data, nil
}

func (p *partition) insertionPoint(id codec.UUIDv7) (int, bool) {
	n := len(p.pending)
	lower := max(0, n-8)
	for i := n - 1; i >= lower; i-- {
		cmp := bytes.Compare(p.pending[i].Id[:], id[:])
		if cmp == 0 {
			return i, true
		}
		if cmp < 0 {
			return i + 1, false
		}
	}
	pos := sort.Search(lower, func(i int) bool { return bytes.Compare(p.pending[i].Id[:], id[:]) >= 0 })
	return pos, pos < lower && p.pending[pos].Id == id
}
