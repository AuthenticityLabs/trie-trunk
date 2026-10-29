package cache

import (
	"encoding/json"
	"fmt"
	"hash/crc32"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LateEventError is a permanent rejection, not an acknowledgement. The sender
// may reconcile with the origin; blindly retrying cannot reopen a closed range.
type LateEventError struct {
	EventID         [16]byte
	FinalizedBefore int64
}

func (e *LateEventError) Error() string {
	return fmt.Sprintf("event %x precedes finalized boundary %s; full event saved in lost.log", e.EventID, time.UnixMilli(e.FinalizedBefore).UTC().Format(time.RFC3339Nano))
}

// Rejection retains the exact original PIL bytes with their immutable map
// binding. This receipt-time audit log is separate from event-time branches and
// is deliberately excluded from branch retention and normal event aggregates.
type Rejection struct {
	ReceivedAt        time.Time `json:"received_at"`
	Branch            string    `json:"branch"`
	EventID           string    `json:"event_id"`
	DefinitionVersion string    `json:"definition_version"`
	FinalizedBefore   time.Time `json:"finalized_before"`
	Reason            string    `json:"reason"`
	PIL               []byte    `json:"pil_base64"`
}

const rejectionLimit = MaxFrame + 4096

func (s *Store) rejectionFile() (*os.File, error) {
	if s.audit != nil {
		return s.audit, nil
	}
	f, e := openFile(filepath.Join(s.dir, "lost.log"), "NUTSREJ6", s.layout.Digest, [16]byte{}, 0)
	if e != nil {
		return nil, e
	}
	if e = boundedRecords(f, rejectionLimit, func(raw []byte) error { var r Rejection; return decoder.Unmarshal(raw, &r) }); e != nil {
		f.Close()
		return nil, e
	}
	s.audit = f
	return f, nil
}
func (s *Store) rejectLate(b *branchStore, id [16]byte, raw []byte) error {
	rejection := Rejection{ReceivedAt: s.config.Now().UTC(), Branch: b.branch.Name(), EventID: fmt.Sprintf("%x", id), DefinitionVersion: fmt.Sprintf("%x", s.layout.Digest), FinalizedBefore: time.UnixMilli(b.ingressBoundary()).UTC(), Reason: "arrival_after_finalization", PIL: raw}
	data, e := encoder.Marshal(rejection)
	if e != nil {
		return s.fail(e)
	}
	f, e := s.rejectionFile()
	if e != nil {
		return s.fail(e)
	}
	e = appendBoundedRecord(f, data, rejectionLimit)
	if e == nil {
		e = f.Sync()
	}
	if e == nil {
		e = syncDir(s.dir)
	}
	if e != nil {
		return s.fail(fmt.Errorf("could not preserve rejected event: %w", e))
	}
	if b.branch == Write || b.branch == Error {
		if e = s.invalidateReceived(Error, raw); e != nil {
			return s.fail(e)
		}
	}
	late := &LateEventError{EventID: id, FinalizedBefore: b.ingressBoundary()}
	log.Printf("ALERT trie-trunk cache=%s collection=%s branch=%s: %v", s.config.Cache, s.binding, b.branch.Name(), late)
	return late
}

// Rejections streams the audit without expanding ordinary collection events.
// It captures a committed byte boundary and an independent descriptor before
// releasing the writer lock. Slow inspection cannot block ingestion.
func (s *Store) Rejections(visit func(Rejection) error) error {
	s.mu.Lock()
	if e := s.check(); e != nil {
		s.mu.Unlock()
		return e
	}
	f, e := s.rejectionFile()
	if e != nil {
		s.mu.Unlock()
		return e
	}
	info, e := f.Stat()
	if e != nil {
		s.mu.Unlock()
		return e
	}
	reader, e := os.Open(f.Name())
	s.mu.Unlock()
	if e != nil {
		return e
	}
	defer reader.Close()
	var header [8]byte
	for at := int64(headerSize); at < info.Size(); {
		if info.Size()-at < 8 {
			return fmt.Errorf("incomplete rejection header")
		}
		if _, e = reader.ReadAt(header[:], at); e != nil {
			return e
		}
		size := int64(order.Uint32(header[:4]))
		if size <= 0 || size > rejectionLimit || size > info.Size()-at-8 {
			return fmt.Errorf("invalid rejection boundary")
		}
		raw := make([]byte, int(size))
		if _, e = reader.ReadAt(raw, at+8); e != nil {
			return e
		}
		if crc32.Checksum(raw, checksum) != order.Uint32(header[4:]) {
			return fmt.Errorf("rejection checksum mismatch")
		}
		var record Rejection
		if e := decoder.Unmarshal(raw, &record); e != nil {
			return e
		}
		if e = visit(record); e != nil {
			return e
		}
		at += 8 + size
	}
	return nil
}

// rejectInput records the original bytes even when no valid UUID can be read.
// Caller holds the store lock. This audit is independent of branch retention.
func (s *Store) rejectInput(branch Branch, raw []byte, reason string, cause error) error {
	rejection := Rejection{ReceivedAt: s.config.Now().UTC(), Branch: branch.Name(), DefinitionVersion: fmtDigestNative(s.layout.Digest), Reason: reason + ": " + cause.Error(), PIL: append([]byte(nil), raw...)}
	data, e := encoder.Marshal(rejection)
	if e != nil {
		return s.fail(e)
	}
	f, e := s.rejectionFile()
	if e == nil {
		e = appendBoundedRecord(f, data, rejectionLimit)
	}
	if e == nil {
		e = f.Sync()
	}
	if e == nil {
		e = syncDir(s.dir)
	}
	if e != nil {
		return s.fail(fmt.Errorf("could not preserve rejected input: %w", e))
	}
	log.Printf("ALERT trie-trunk cache=%s resource=%s branch=%s lost event: %s", s.config.Cache, s.binding, branch.Name(), rejection.Reason)
	return fmt.Errorf("%s; original input saved in lost.log", rejection.Reason)
}

// Expanded only for explicit audit inspection. Stored/transmitted source bytes
// remain intact, including sparse fields and exact integer/PIL representations.
func (l *Layout) inspect(raw []byte) (map[string]any, error) {
	var f Frame
	if e := l.Scan(raw, &f); e != nil {
		return nil, e
	}
	fields := map[string]any{}
	for i, v := range f.Values {
		field := l.Definition.Fields[i]
		if v.State == Absent {
			continue
		}
		if v.State == Null {
			fields[field.Name] = nil
			continue
		}
		if len(field.Values) > 0 && v.State == Direct {
			fields[field.Name] = field.Values[unsigned(v.Bytes)]
			continue
		}
		fields[field.Name] = inspectValue(field.Type, v.Bytes)
	}
	return fields, nil
}
func inspectValue(kind string, b []byte) any {
	switch kind {
	case "STRING":
		return string(b)
	case "UUID":
		return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
	case "BOOL":
		return b[0] != 0
	case "U8", "U16", "U24", "U32", "U64", "REF16":
		return unsigned(b)
	case "I8", "I16", "I32", "I64", "TIMESTAMP_MS", "TIMESTAMP_US", "TIMESTAMP_NS":
		n := unsigned(b)
		if len(b) < 8 && b[len(b)-1]&128 != 0 {
			n |= ^uint64(0) << (len(b) * 8)
		}
		return int64(n)
	case "F32", "F64":
		var f float64
		if kind == "F32" {
			f = float64(math.Float32frombits(order.Uint32(b)))
		} else {
			f = math.Float64frombits(order.Uint64(b))
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return fmt.Sprint(f)
		}
		return f
	case "OBJECT":
		var value any
		if decoder.Unmarshal(b, &value) == nil {
			value = jsonObject(value)
			if _, e := json.Marshal(value); e == nil {
				return value
			}
		}
		return map[string]any{"pil_base64": b}
	}
	if strings.HasPrefix(kind, "ARRAY<") {
		element := kind[6 : len(kind)-1]
		at := 0
		n, _ := readExtended(b, &at)
		w, _ := width(element)
		out := make([]any, int(n))
		if w > 0 {
			for i := range out {
				out[i] = inspectValue(element, b[at+i*w:at+(i+1)*w])
			}
		} else {
			ends := make([]uint64, int(n))
			for i := range ends {
				ends[i], _ = readExtended(b, &at)
			}
			start := 0
			for i, end := range ends {
				out[i] = inspectValue(element, b[at+start:at+int(end)])
				start = int(end)
			}
		}
		return out
	}
	return b
}

// Convert named PIL maps recursively only at the explicit JSON inspection edge.
// Maps with non-string keys retain the lossless PIL fallback instead of
// coercing keys and potentially losing distinctions.
func jsonObject(value any) any {
	switch v := value.(type) {
	case map[any]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			name, ok := k.(string)
			if !ok {
				return value
			}
			out[name] = jsonObject(x)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = jsonObject(x)
		}
		return out
	default:
		return value
	}
}
