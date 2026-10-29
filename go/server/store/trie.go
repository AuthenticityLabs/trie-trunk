package store

import (
	"fmt"
	"os"
	"path/filepath"

	pil "github.com/AuthenticityLabs/pill-packer/go/codec"
	"trie-trunk/go/codec"
)

type node struct {
	Parent uint32
	Key    codec.Key
	Value  []byte
}

type edge struct {
	Parent uint32
	Key    codec.Key
	Value  string
}

type trie struct {
	file   *os.File
	schema *schema
	nodes  []node
	edges  map[edge]uint32
	users  int
}

func acquireTrie(c Config, s *schema) (*trie, error) {
	if s.trie != nil {
		s.trie.users++
		return s.trie, nil
	}
	dir := s.directory(c.Root, c.Tap)
	f, err := s.openLog(filepath.Join(dir, s.filename("tri")), "TTRI")
	if err != nil {
		return nil, err
	}
	t := &trie{file: f, schema: s, nodes: []node{{}}, edges: make(map[edge]uint32), users: 1}
	err = scan(f, func(_, _ int64, b []byte) error {
		if len(b) < 7 {
			return fmt.Errorf("short trie node")
		}
		if len(t.nodes) >= c.MaxNodes || s.narrowReferences() && len(t.nodes) >= 1<<16 {
			return ErrLimit
		}
		n := node{order.Uint32(b[:4]), codec.Key(order.Uint16(b[4:6])), append([]byte(nil), b[6:]...)}
		if uint64(n.Parent) >= uint64(len(t.nodes)) {
			return fmt.Errorf("invalid trie parent")
		}
		var value uint64
		if decoder.Unmarshal(n.Value, &value) != nil {
			return fmt.Errorf("invalid trie value")
		}
		if _, ok := s.definition.Fields[n.Key]; !ok {
			return fmt.Errorf("unknown trie field")
		}
		key := edge{n.Parent, n.Key, string(n.Value)}
		if _, ok := t.edges[key]; ok {
			return fmt.Errorf("duplicate trie edge")
		}
		t.edges[key] = uint32(len(t.nodes))
		t.nodes = append(t.nodes, n)
		return nil
	})
	if err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = syncDirectory(dir)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	s.trie = t
	return t, nil
}

func (t *trie) release() error {
	t.users--
	if t.users != 0 {
		return nil
	}
	t.schema.trie = nil
	return t.file.Close()
}

func (tr *trie) prepare(r record, c Config) (record, error) {
	var e error
	parent := uint32(0)
	for _, t := range r.Path {
		key := edge{parent, t.Key, string(t.Value)}
		if next, ok := tr.edges[key]; ok {
			parent = next
			continue
		}

		if len(tr.nodes) >= c.MaxNodes || tr.schema.narrowReferences() && len(tr.nodes) >= 1<<16 {
			var fields map[codec.Key]pil.RawObject
			if e = decoder.Unmarshal(r.Dynamic, &fields); e != nil {
				return r, e
			}
			for _, remaining := range r.Path {
				fields[remaining.Key] = remaining.Value
			}
			r.Dynamic, e = diskEncoder.Marshal(fields)
			if e != nil {
				return r, e
			}
			parent = 0
			break
		}
		b := make([]byte, 6+len(t.Value))
		order.PutUint32(b[:4], parent)
		order.PutUint16(b[4:6], uint16(t.Key))
		copy(b[6:], t.Value)
		if _, e = appendBlock(tr.file, b); e != nil {
			return r, e
		}
		next := uint32(len(tr.nodes))
		tr.edges[key] = next
		tr.nodes = append(tr.nodes, node{parent, t.Key, t.Value})
		parent = next
	}

	r.Leaf, r.Prepared = parent, true
	return r, nil
}
