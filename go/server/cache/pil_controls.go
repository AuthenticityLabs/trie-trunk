package cache

import (
	"fmt"
	pil "github.com/AuthenticityLabs/pill-packer/go/codec"
	"io"
)

// TPIL control schemas and tags are Trie-Trunk protocol choices. Their wire
// encoding is the same definition-driven PIL codec used by data seeds.
func controlLayout(types ...string) *pil.Layout {
	d := pil.Definition{Identity: "trie-trunk/control"}
	for i, t := range types {
		d.Fields = append(d.Fields, pil.Field{Name: fmt.Sprintf("field_%d", i), Type: t})
	}
	l, err := pil.Compile(d)
	if err != nil {
		panic(err)
	}
	return l
}

var defineControl = controlLayout("BYTES", "BYTES", "BYTES")
var bindControl = controlLayout("U32", "BYTES", "STRING")
var manifestControl = controlLayout("U32", "OBJECT")
var chunkControl = controlLayout("U32", "U8", "U64", "BYTES", "BYTES")
var endControl = controlLayout("U32")
var viewControl = controlLayout("BYTES")
var trunkControl = controlLayout("BYTES")

func writeControl(w io.Writer, tag byte, l *pil.Layout, values ...pil.Value) error {
	if n, err := w.Write([]byte{tag}); err != nil {
		return err
	} else if n != 1 {
		return io.ErrShortWrite
	}
	return l.Write(w, values)
}
func bytesValue(b []byte) pil.Value { return pil.Value{State: pil.Offset, Bytes: b} }
