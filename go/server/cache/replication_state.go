package cache

import (
	"bytes"
	"fmt"
	"sort"
)

type stateEntry struct {
	ID    []byte
	Token uint64
}
type statePage struct {
	Epoch                [16]byte
	Sequence, Collection uint64
	Objects              []stateEntry
	Paged                bool `pil:",omitempty"`
	Final                bool `pil:",omitempty"`
	Unchanged            bool `pil:",omitempty"`
}
type stateTransfer struct{ statePage }

// Copy a stable snapshot under the writer mutex; encode pages after releasing it.
// Object lookup compares both the collection and object components of its token.
func (s *Store) snapshotState(have Coverage, paged bool) (stateTransfer, error) {
	st := s.state
	page := statePage{Epoch: st.epoch, Sequence: st.sequence, Collection: st.collection, Paged: paged}
	if paged && have.StateKnown && have.StateEpoch == st.epoch && have.StateSequence == st.sequence {
		page.Unchanged, page.Final = true, true
		return stateTransfer{page}, nil
	}
	if !paged && len(st.objects) > 65536 {
		return stateTransfer{}, fmt.Errorf("native-state-pages capability required")
	}
	page.Objects = make([]stateEntry, 0, len(st.objects))
	for id, token := range st.objects {
		page.Objects = append(page.Objects, stateEntry{[]byte(id), token})
	}
	sort.Slice(page.Objects, func(i, j int) bool { return bytes.Compare(page.Objects[i].ID, page.Objects[j].ID) < 0 })
	return stateTransfer{page}, nil
}
func (s stateTransfer) send(emit func(uint64, []byte) error) error {
	return s.visitPages(func(start uint64, page statePage) error {
		raw, err := encoder.Marshal(page)
		if err != nil {
			return err
		}
		return emit(start, raw)
	})
}
func (s stateTransfer) visitPages(emit func(uint64, statePage) error) error {
	if !s.Paged || s.Unchanged {
		return emit(0, s.statePage)
	}
	for start := 0; ; {
		end, size := start, 0
		for end < len(s.Objects) && end-start < 1024 {
			n := len(s.Objects[end].ID) + 64
			if end > start && size+n > 1<<20 {
				break
			}
			size += n
			end++
		}
		page := s.statePage
		page.Objects, page.Final = s.Objects[start:end], end == len(s.Objects)
		if err := emit(uint64(start), page); err != nil {
			return err
		}
		if page.Final {
			return nil
		}
		start = end
	}
}
