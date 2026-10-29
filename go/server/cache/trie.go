package cache

import "fmt"

type sharedNode struct {
	Parent uint16 `pil:"0"`
	Field  uint16 `pil:"1"`
	Bytes  []byte `pil:"2"`
}
type sharedKey struct {
	Parent uint16
	Field  uint16
	Bytes  string
}
type sharedTrie struct {
	nodes     []sharedNode
	edges     map[sharedKey]uint16
	persisted int
}

func newTrie() sharedTrie {
	return sharedTrie{nodes: []sharedNode{{}}, edges: map[sharedKey]uint16{}, persisted: 1}
}
func (t *sharedTrie) add(n sharedNode, l *Layout) error {
	if len(t.nodes) >= 65536 || int(n.Parent) >= len(t.nodes) || int(n.Field) >= len(l.Widths) || !l.ValidDirectWidth(int(n.Field), n.Bytes) || len(n.Bytes) == 0 {
		return fmt.Errorf("invalid shared node")
	}
	depth := 0
	for p := n.Parent; p != 0; p = t.nodes[p].Parent {
		depth++
	}
	if depth >= len(l.Path) || l.Path[depth] != int(n.Field) {
		return fmt.Errorf("invalid shared path")
	}
	key := sharedKey{n.Parent, n.Field, string(n.Bytes)}
	if _, ok := t.edges[key]; ok {
		return fmt.Errorf("duplicate shared node")
	}
	n.Bytes = append([]byte(nil), n.Bytes...)
	t.edges[key] = uint16(len(t.nodes))
	t.nodes = append(t.nodes, n)
	return nil
}

// Only the present contiguous prefix is shared. Absent/NULL/literal fields stop
// the path; expansion never turns an absent PATCH field into an assignment.
func (t *sharedTrie) intern(l *Layout, v []Value) uint16 {
	var parent uint16
	for _, i := range l.Path {
		if v[i].State != Direct {
			break
		}
		k := sharedKey{parent, uint16(i), string(v[i].Bytes)}
		next, ok := t.edges[k]
		if !ok {
			if len(t.nodes) == 65536 {
				break
			}
			next = uint16(len(t.nodes))
			t.nodes = append(t.nodes, sharedNode{parent, uint16(i), append([]byte(nil), v[i].Bytes...)})
			t.edges[k] = next
		}
		parent = next
	}
	return parent
}
func (t *sharedTrie) remove(ref uint16, v []Value) {
	for ref != 0 {
		n := t.nodes[ref]
		v[n.Field] = Value{}
		ref = n.Parent
	}
}
func (t *sharedTrie) expand(ref uint16, v []Value) error {
	if int(ref) >= len(t.nodes) {
		return fmt.Errorf("unknown shared reference")
	}
	for ref != 0 {
		n := t.nodes[ref]
		if v[n.Field].State != Absent {
			return fmt.Errorf("payload shadows shared reference")
		}
		v[n.Field] = Value{Direct, n.Bytes}
		ref = n.Parent
	}
	return nil
}

// expandShared resolves only the explicitly bound storage REF16 field. Definitions
// without this role retain their ordinary PIL fields and need no trie projection.
func (s *Store) expandShared(values []Value) error {
	return expandShared(s.layout, &s.trie, values)
}
func expandShared(l *Layout, trie *sharedTrie, values []Value) error {
	if l.Shared < 0 {
		return nil
	}
	v := values[l.Shared]
	if v.State == Absent {
		return nil
	}
	if v.State != Direct || len(v.Bytes) != 2 {
		return fmt.Errorf("invalid shared REF16")
	}
	if err := trie.expand(order.Uint16(v.Bytes), values); err != nil {
		return err
	}
	values[l.Shared] = Value{}
	return nil
}

func (t *sharedTrie) lookup(l *Layout, v []Value) uint16 {
	var parent uint16
	for _, i := range l.Path {
		if v[i].State != Direct {
			break
		}
		next, ok := t.edges[sharedKey{parent, uint16(i), string(v[i].Bytes)}]
		if !ok {
			break
		}
		parent = next
	}
	return parent
}

// Nodes and value bytes are immutable once inserted; only the slice and edge
// directory need private backing while a sealing worker appends new nodes.
func (t *sharedTrie) clone() sharedTrie {
	out := sharedTrie{nodes: append([]sharedNode(nil), t.nodes...), edges: make(map[sharedKey]uint16, len(t.edges)), persisted: t.persisted}
	for k, v := range t.edges {
		out.edges[k] = v
	}
	return out
}
