package cache

// Object invalidation tokens and their durable journal, separate from revision postings.
import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
)

type StateToken struct {
	Epoch              [16]byte
	Collection, Object uint64
}
type invalidation struct {
	Sequence      uint64
	Objects       [][]byte
	Collection    bool
	Snapshot      *statePage `cbor:",omitempty"`
	SnapshotStart uint64     `cbor:",omitempty"`
}
type objectState struct {
	file                        *os.File
	epoch                       [16]byte
	sequence, collection        uint64
	objects                     map[string]uint64
	journalBytes, identityBytes int64
	failed                      error
}

func (s *Store) openState() error {
	if g := s.config.group; g != nil && g.states[s.collection] != nil {
		s.state = g.states[s.collection]
		return nil
	}
	path := filepath.Join(s.collection, "collection.state")
	var epoch [16]byte
	magic := "NUTSSTA6"
	f, e := os.Open(path)
	if e == nil {
		var h [headerSize]byte
		_, e = f.Read(h[:])
		f.Close()
		if e != nil {
			return e
		}
		copy(epoch[:], h[40:56])
		if string(h[:8]) == "NUTSSTC6" {
			magic = "NUTSSTC6"
		}
	} else if os.IsNotExist(e) {
		if _, e = rand.Read(epoch[:]); e != nil {
			return e
		}
	} else {
		return e
	}
	f, e = openFile(path, magic, [32]byte{}, epoch, 0)
	if e != nil {
		return e
	}
	s.state = &objectState{file: f, epoch: epoch, objects: map[string]uint64{}}
	checkpoint := stateCheckpoint{}
	err := records(f, func(raw []byte) error {
		var v invalidation
		if e := decoder.Unmarshal(raw, &v); e != nil {
			return e
		}
		if v.Snapshot != nil {
			return checkpoint.apply(s.state, v)
		}
		if v.SnapshotStart != 0 {
			return fmt.Errorf("checkpoint offset without checkpoint")
		}
		if checkpoint.started && !checkpoint.complete {
			return fmt.Errorf("incomplete invalidation checkpoint")
		}
		if v.Sequence != s.state.sequence+1 {
			return fmt.Errorf("invalidation sequence gap")
		}
		s.state.sequence = v.Sequence
		if v.Collection {
			s.state.collection = v.Sequence
		} else {
			for _, object := range v.Objects {
				s.state.objects[string(object)] = v.Sequence
			}
		}
		return nil
	})
	if err == nil && (checkpoint.started || magic == "NUTSSTC6") && !checkpoint.complete {
		err = fmt.Errorf("incomplete invalidation checkpoint")
	}
	if err != nil {
		f.Close()
		s.state = nil
		return err
	}
	stat, e := f.Stat()
	if e != nil {
		f.Close()
		s.state = nil
		return e
	}
	s.state.journalBytes = stat.Size()
	for id := range s.state.objects {
		s.state.identityBytes += int64(len(id))
	}
	if err = s.removeStateTemps(); err != nil {
		f.Close()
		s.state = nil
		return err
	}
	if g := s.config.group; g != nil {
		g.states[s.collection] = s.state
	}
	return nil
}
func (s *Store) invalidate(objects [][]byte, collection bool) error {
	st := s.state
	if st.sequence == ^uint64(0) {
		return fmt.Errorf("invalidation sequence exhausted")
	}
	v := invalidation{Sequence: st.sequence + 1, Objects: objects, Collection: collection}
	raw, e := encoder.Marshal(v)
	if e != nil {
		return e
	}
	if e = appendRecord(st.file, raw); e != nil {
		return e
	}
	if e = st.file.Sync(); e != nil {
		return e
	}
	st.journalBytes += int64(len(raw) + 8)
	st.sequence = v.Sequence
	if collection {
		st.collection = v.Sequence
	} else {
		for _, object := range objects {
			if _, exists := st.objects[string(object)]; !exists {
				st.identityBytes += int64(len(object))
			}
			st.objects[string(object)] = v.Sequence
		}
	}
	// Compact repeated invalidations without invalidating otherwise unchanged
	// objects or resetting tokens. Cost is amortized over journal growth.
	if st.journalBytes > st.compactionThreshold() {
		return s.compactStateLocked()
	}
	return nil
}
func (s *Store) token(id Value) StateToken {
	if s.layout.Object >= 0 {
		id = s.layout.identityValue(s.layout.Object, id)
	}
	return StateToken{Epoch: s.state.epoch, Collection: s.state.collection, Object: s.state.objects[string(id.Bytes)]}
}

func (s *Store) validateObject(id Value) error {
	if s.layout.Object < 0 {
		return fmt.Errorf("no object identity declared")
	}
	w := s.layout.Widths[s.layout.Object]
	if w > 0 {
		if id.State != Direct || !s.layout.validDirectWidth(s.layout.Object, s.layout.shortValue(s.layout.Object, id).Bytes) {
			return fmt.Errorf("object identity type mismatch")
		}
	} else if id.State != Offset {
		return fmt.Errorf("object identity type mismatch")
	}
	return nil
}
func (s *Store) Token(id Value) (StateToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return StateToken{}, e
	}
	if e := s.validateObject(id); e != nil {
		return StateToken{}, e
	}
	return s.token(id), nil
}

// StillCurrent guards a client's origin refresh. It does not clear state shared
// with other clients, and a concurrent newer invalidation makes it return false.
func (s *Store) StillCurrent(id Value, observed StateToken) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return false, e
	}
	if e := s.validateObject(id); e != nil {
		return false, e
	}
	return observed == s.token(id), nil
}
