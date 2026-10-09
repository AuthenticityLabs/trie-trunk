package store

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"trie-trunk/go/codec"
)

func (p *partition) watermark() int64 {
	if p.schema.modern() {
		return p.popEnd
	}
	return p.indexEnd
}
func (p *partition) accepts(r row) bool {
	if !p.schema.modern() {
		return dayOf(r.Id).Equal(p.day)
	}
	at := time.UnixMilli(millis(r.Id))
	return !at.Before(p.scope.Start) && at.Before(p.scope.End) && p.schema.owner(p.trie, r.Leaf) == p.scope.Owner
}
func (p *partition) scopeBytes() []byte {
	b := make([]byte, 20)
	order.PutUint64(b[:8], uint64(p.scope.Start.UnixMilli()))
	order.PutUint64(b[8:16], uint64(p.scope.End.UnixMilli()))
	order.PutUint32(b[16:], p.scope.Owner)
	return b
}
func (p *partition) checkScope(b []byte) error {
	if !bytes.Equal(b, p.scopeBytes()) {
		return fmt.Errorf("index scope mismatch")
	}
	return nil
}
func (p *partition) openIndex() (*os.File, error) {
	path := filepath.Join(p.dir, p.schema.filename("idx"))
	if !p.schema.modern() {
		return openLog(path, "TIDX")
	}
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		p.compact = !p.scope.End.After(p.config.Now().Add(-matureDelay))
		tmp, e := os.CreateTemp(p.dir, ".index-create-*")
		if e != nil {
			return nil, e
		}
		name := tmp.Name()
		defer os.Remove(name)
		var h [8]byte
		copy(h[:4], "TIDA")
		if p.compact {
			copy(h[:4], "TIDC")
		}
		order.PutUint32(h[4:], p.schema.definition.Storage.Format)
		_, e = tmp.Write(h[:])
		if e == nil {
			_, e = appendBlock(tmp, p.scopeBytes())
		}
		if e == nil {
			e = tmp.Sync()
		}
		closeErr := tmp.Close()
		if e == nil {
			e = closeErr
		}
		if e == nil {
			e = os.Rename(name, path)
		}
		if e == nil {
			e = syncDirectory(p.dir)
		}
		if e != nil {
			return nil, e
		}
	} else if err != nil {
		return nil, err
	}
	sc, compact, err := readScope(p.dir)
	if err != nil {
		return nil, err
	}
	if !sc.Start.Equal(p.scope.Start) || !sc.End.Equal(p.scope.End) || sc.Owner != p.scope.Owner {
		return nil, fmt.Errorf("index scope mismatch")
	}
	p.compact = compact
	p.indexEnd = fileHeaderSize + indexMetadataSize
	magic := "TIDA"
	if compact {
		magic = "TIDC"
	}
	return p.schema.openLog(path, magic)
}

// Disk references are U16 in format 4. Keep in-memory ordinals wide for direct
// lookup and compatibility with older stores; never truncate on serialization.
func (s *schema) narrowReferences() bool {
	return s != nil && s.definition.Storage != nil && s.definition.Storage.Format == 4
}
func (p *partition) rowLayout() (int, int) {
	width, header := indexRowSize, 4
	if p.compact {
		width, header = compactRowSize, 12
	}
	if p.schema.narrowReferences() {
		width -= 2
	}
	return width, header
}
func (p *partition) encodeRun(rows []row) ([]byte, error) {
	if len(rows) == 0 {
		return nil, fmt.Errorf("empty index run")
	}
	if !p.compact && !p.schema.narrowReferences() {
		return encodeRun(rows), nil
	}
	width, header := p.rowLayout()
	refWidth := 4
	if p.schema.narrowReferences() {
		refWidth = 2
	}
	b := make([]byte, header+width*len(rows))
	order.PutUint32(b[:4], uint32(len(rows)))
	if p.compact {
		order.PutUint64(b[4:12], rows[0].Start)
	}
	for i, r := range rows {
		pos := header + i*width
		copy(b[pos:pos+16], r.Id[:])
		if refWidth == 2 {
			if r.Leaf > 65535 {
				return nil, fmt.Errorf("trie reference exceeds U16")
			}
			order.PutUint16(b[pos+16:pos+18], uint16(r.Leaf))
		} else {
			order.PutUint32(b[pos+16:pos+20], r.Leaf)
		}
		if !p.compact {
			order.PutUint64(b[pos+16+refWidth:pos+width-8], r.Start)
		}
		order.PutUint64(b[pos+width-8:pos+width], r.End)
	}
	return b, nil
}
func (p *partition) decodeRun(b []byte) ([]row, error) {
	if !p.compact && !p.schema.narrowReferences() {
		return decodeRun(b)
	}
	width, header := p.rowLayout()
	if len(b) < header {
		return nil, fmt.Errorf("short index run")
	}
	n := int(order.Uint32(b[:4]))
	if n == 0 || n > (len(b)-header)/width || len(b) != header+n*width {
		return nil, fmt.Errorf("invalid index run length")
	}
	rows := make([]row, n)
	refWidth := 4
	if p.schema.narrowReferences() {
		refWidth = 2
	}
	var start uint64
	if p.compact {
		start = order.Uint64(b[4:12])
	}
	for i := range rows {
		pos := header + i*width
		var raw [indexRowSize]byte
		copy(raw[:16], b[pos:pos+16])
		if refWidth == 2 {
			order.PutUint32(raw[16:20], uint32(order.Uint16(b[pos+16:pos+18])))
		} else {
			copy(raw[16:20], b[pos+16:pos+20])
		}
		if !p.compact {
			start = order.Uint64(b[pos+16+refWidth : pos+width-8])
		}
		order.PutUint64(raw[20:28], start)
		copy(raw[28:], b[pos+width-8:pos+width])
		v, err := decodeRow(raw[:])
		if err != nil {
			return nil, err
		}
		if i > 0 && (rows[i-1].End != v.Start || bytes.Compare(rows[i-1].Id[:], v.Id[:]) >= 0) {
			return nil, fmt.Errorf("unordered index run")
		}
		rows[i] = v
		start = v.End
	}
	return rows, nil
}
func (p *partition) mature(now time.Time) error {
	if !p.schema.modern() || p.compact || p.scope.End.After(now.Add(-matureDelay)) {
		return nil
	}
	if len(p.pending) != 0 {
		return fmt.Errorf("cannot mature pending index")
	}
	tmp, err := os.CreateTemp(p.dir, ".index-mature-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	defer tmp.Close()
	var h [8]byte
	copy(h[:4], "TIDC")
	order.PutUint32(h[4:], p.schema.definition.Storage.Format)
	if _, err = tmp.Write(h[:]); err != nil {
		return err
	}
	if _, err = appendBlock(tmp, p.scopeBytes()); err != nil {
		return err
	}
	runs := make([]run, 0, len(p.runs))
	for _, old := range p.runs {
		data := make([]byte, old.End-old.Offset-8)
		if _, err = p.idx.ReadAt(data, old.Offset+8); err != nil {
			return err
		}
		var frame [8]byte
		if _, err = p.idx.ReadAt(frame[:], old.Offset); err != nil {
			return err
		}
		if int64(order.Uint32(frame[:4])) != int64(len(data)) || checksum(data) != order.Uint32(frame[4:]) {
			return fmt.Errorf("source index checksum mismatch")
		}
		rows, e := p.decodeRun(data)
		if e != nil {
			return e
		}
		start, e := tmp.Seek(0, 2)
		if e != nil {
			return e
		}
		encoder := partition{compact: true, schema: p.schema}
		encoded, e := encoder.encodeRun(rows)
		if e != nil {
			return e
		}
		end, e := appendBlock(tmp, encoded)
		if e != nil {
			return e
		}
		runs = append(runs, run{start, end, old.Count, old.Min, old.Max})
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	// Open the new inode before publishing it; existing readers retain the old inode.
	next, err := os.OpenFile(name, os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(p.dir, p.schema.filename("idx"))); err != nil {
		next.Close()
		return err
	}
	old := p.idx
	p.idx = next
	p.compact = true
	p.runs = runs
	p.indexEnd = fileHeaderSize + indexMetadataSize
	if len(runs) > 0 {
		p.indexEnd = runs[len(runs)-1].End
	}
	closeErr := old.Close()
	if err = syncDirectory(p.dir); err != nil {
		return err
	}
	return closeErr
}
func (m *Manager) containsScoped(s *schema, id codec.UUIDv7, sc scope) (bool, error) {
	key := fmt.Sprintf("%x/%s", s.revision.Digest, s.day(time.UnixMilli(millis(id))).Format("2006-01-02"))
	maxID := m.maximum[key]
	if bytes.Compare(id[:], maxID[:]) > 0 {
		return false, nil
	}
	for _, other := range m.known[s.revision.Digest] {
		at := time.UnixMilli(millis(id))
		if at.Before(other.Start) || !at.Before(other.End) {
			continue
		}
		p, err := m.partitionAt(s, other)
		if err != nil {
			return false, err
		}
		if _, duplicate := p.insertionPoint(id); duplicate {
			return true, nil
		}
		if duplicate, err := p.contains(id); duplicate || err != nil {
			return duplicate, err
		}
	}
	return false, nil
}
func (m *Manager) remember(s *schema, p *partition) {
	if !s.modern() {
		return
	}
	if m.known[s.revision.Digest] == nil {
		m.known[s.revision.Digest] = make(map[string]scope)
	}
	m.known[s.revision.Digest][p.scope.Dir] = p.scope
	if m.active[s.revision.Digest] == nil {
		m.active[s.revision.Digest] = make(map[string]scope)
	}
	if p.compact {
		delete(m.active[s.revision.Digest], p.scope.Dir)
	} else {
		m.active[s.revision.Digest][p.scope.Dir] = p.scope
	}

	m.rememberID(s, p.committedMax)
}
func (m *Manager) rememberID(s *schema, id codec.UUIDv7) {
	if !s.modern() || !id.Valid() {
		return
	}
	key := fmt.Sprintf("%x/%s", s.revision.Digest, s.day(time.UnixMilli(millis(id))).Format("2006-01-02"))
	previous := m.maximum[key]
	if bytes.Compare(id[:], previous[:]) > 0 {
		m.maximum[key] = id
	}
}
func (m *Manager) scanScoped(s *schema, day time.Time, visit func(codec.Record) error) error {
	start, end := s.dayBounds(day)
	for _, sc := range m.known[s.revision.Digest] {
		if sc.Start.Before(end) && sc.End.After(start) {
			p, err := m.partitionAt(s, sc)
			if err != nil {
				return err
			}
			if err = scanPartition(p, s, visit); err != nil {
				return err
			}
		}
	}
	return nil
}
func (m *Manager) summariesScoped(s *schema, day time.Time) ([]Summary, error) {
	start, end := s.dayBounds(day)
	var result []Summary
	for _, sc := range m.known[s.revision.Digest] {
		if !sc.Start.Before(end) || !sc.End.After(start) {
			continue
		}
		p, err := m.partitionAt(s, sc)
		if err != nil {
			return nil, err
		}
		err = scan(p.sum, func(_, _ int64, b []byte) error {
			v, err := s.decodeSummary(b)
			if err == nil {
				result = append(result, v)
			}
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
