package cache

import (
	"context"
	"fmt"
	"io"
	"time"
)

// A live window borrows the same Definition as durable packs. Only its storage
// lifetime differs: these rows never certify a sealed hour on the client.
type liveWindow struct {
	generation       [16]byte
	sequence         uint64
	sealed, retained int64
	events           []pending
}

// Capture the requested interval once. One arena copy keeps the writer lock
// short and owns the frames while the response is packed outside that lock.
func (s *Store) liveWindow(branch Branch, from, to time.Time) (*liveWindow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return nil, err
	}
	b, err := s.openBranch(branch)
	if err != nil {
		return nil, err
	}
	sealed := min(b.time.sealed(), utcHour(b.live.firstPending()))
	result := &liveWindow{generation: b.generation, sequence: s.generationSequence(branch), sealed: sealed, retained: min(b.time.first(), sealed)}
	result.events = b.pendingWindow(nil, time.UnixMilli(max(from.UnixMilli(), sealed)), to)
	size := 0
	for _, event := range result.events {
		size += len(event.frame)
		if size > 64<<20 {
			return nil, fmt.Errorf("live window exceeds 64 MB; narrow the time window or scope")
		}
	}
	arena := make([]byte, size)
	at := 0
	for i, event := range result.events {
		n := copy(arena[at:], event.frame)
		result.events[i].frame = arena[at : at+n]
		at += n
	}
	return result, nil
}

// Positional context for the existing collection stream. No query signature,
// predicate, checkpoint, or server-side query receipt is part of this contract.
var liveWindowControl = controlLayout("U32", "BYTES", "U64", "U64", "U64")

func (h *HTTP) writeLiveWindow(ctx context.Context, w io.Writer, stream uint32, branch Branch, window *liveWindow) error {
	if err := writeControl(w, 11, liveWindowControl, Uint(uint64(stream), 4), bytesValue(window.generation[:]), Uint(window.sequence, 8), Uint(uint64(window.sealed), 8), Uint(uint64(window.retained), 8)); err != nil {
		return err
	}
	for at := 0; at < len(window.events); {
		if err := ctx.Err(); err != nil {
			return err
		}
		hour := utcHour(eventTime(window.events[at].id[:]))
		end := at
		for end < len(window.events) && utcHour(eventTime(window.events[end].id[:])) == hour {
			end++
		}
		p, err := h.Store.livePack(branch, hour, window.events[at:end])
		if err != nil {
			return err
		}
		if _, err = w.Write([]byte{7}); err != nil {
			return err
		}
		if _, err = w.Write(p.Bytes); err != nil {
			return err
		}
		at = end
	}
	return nil
}

// The Seed application owns time/index binding; Pill Packer owns factoring,
// addressing and serialization. These ordinary PACs are left uncompressed.
func (s *Store) livePack(branch Branch, hour int64, events []pending) (*PAC, error) {
	frames := make([][]byte, len(events))
	rows := make([]byte, len(events)*6)
	var frame Frame
	for i, event := range events {
		if err := s.layout.Scan(event.frame, &frame); err != nil {
			return nil, err
		}
		frame.Values[s.layout.Event] = Value{}
		frame.Values[s.layout.Time] = Value{}
		frame.Values[s.layout.Leaf] = Value{}
		// Keep the ingress ordinal until sealing assigns the final row number.
		raw, err := s.layout.Encode(nil, frame.Values)
		if err != nil {
			return nil, err
		}
		frames[i] = raw
		order.PutUint32(rows[i*6:], uint32(eventTime(event.id[:])))
	}
	p, err := factorPAC(s.layout, frames, []PACExtension{{Type: PACSource, Data: []byte((&HTTP{Store: s}).tap(branch))}}, true)
	if err != nil {
		return nil, err
	}
	p.Headers = map[string][]byte{"seed.hour": order.AppendUint64(nil, uint64(hour)), "seed.rows": rows}
	p.AddressBytes = 3
	memberBytes := 0
	for _, member := range p.Members {
		memberBytes += len(member)
	}
	if memberBytes >= 1<<24 {
		p.AddressBytes = 4
	}
	p.Bytes, err = p.Marshal()
	return p, err
}
