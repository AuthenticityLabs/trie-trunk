package logging

import (
	"encoding/json"
	"fmt"
	"strconv"
	Keys "trie-trunk/go/logger/client/keys"

	"trie-trunk/go/codec"
	"trie-trunk/go/models"
)

// entryEncoder appends to one output buffer. Only dynamic extension values use
// the general serializer. It never constructs a second entry or checkpoint slice.
type entryEncoder struct {
	data   []byte
	format codec.Format
	err    error
}

type objectMark struct{ offset, count int }

func (w *entryEncoder) object() objectMark {
	mark := objectMark{offset: len(w.data)}
	w.data = append(w.data, '{')
	return mark
}
func (w *entryEncoder) key(mark *objectMark, key codec.Key) {
	if mark.count > 0 {
		w.data = append(w.data, ',')
	}
	w.data = append(w.data, '"')
	w.data = strconv.AppendUint(w.data, uint64(key), 10)
	w.data = append(w.data, '"', ':')
	mark.count++
}
func (w *entryEncoder) end(mark objectMark) { w.data = append(w.data, '}') }
func (w *entryEncoder) uint(n uint64)       { w.data = strconv.AppendUint(w.data, n, 10) }
func (w *entryEncoder) integer(n int64)     { w.data = strconv.AppendInt(w.data, n, 10) }
func (w *entryEncoder) text(text string) {
	data, err := json.Marshal(text)
	if err != nil {
		w.err = err
	}
	w.data = append(w.data, data...)
}
func (w *entryEncoder) symbol(s codec.Symbol) {
	if text, ok := s.Raw().(string); ok {
		w.text(text)
	} else {
		w.uint(uint64(s.Id()))
	}
}
func (w *entryEncoder) symbolField(mark *objectMark, key codec.Key, s codec.Symbol) {
	if !s.IsZero() {
		w.key(mark, key)
		w.symbol(s)
	}
}
func (w *entryEncoder) textField(mark *objectMark, key codec.Key, s string) {
	if s != "" {
		w.key(mark, key)
		w.text(s)
	}
}

func (e *Entry) encodeCompact(format codec.Format) ([]byte, error) {
	if format != codec.CompactJSON {
		return nil, fmt.Errorf("unsupported format %d", format)
	}
	w := entryEncoder{data: make([]byte, 0, 128), format: format}
	root := w.object()
	w.key(&root, Keys.Category)
	w.uint(uint64(e.Category))
	w.key(&root, Keys.Level)
	w.uint(uint64(e.Level))
	w.symbolField(&root, Keys.Service, e.Service)
	w.textField(&root, Keys.Version, e.Version)
	w.symbolField(&root, Keys.Resource, e.Resource)
	w.symbolField(&root, Keys.Operation, e.Operation)
	w.key(&root, Keys.Id)
	w.text(e.Id.String())
	w.textField(&root, Keys.ClientTransactionId, e.ClientTransactionId)
	w.textField(&root, Keys.ClientId, e.ClientId)
	w.key(&root, Keys.Duration)
	w.integer(e.Duration.Microseconds())
	w.symbolField(&root, Keys.HttpMethod, e.HttpMethod)
	if e.present&(1<<Keys.HttpStatus) != 0 {
		w.key(&root, Keys.HttpStatus)
		w.uint(uint64(e.HttpStatus))
	}
	w.symbolField(&root, Keys.Message, e.Message)
	if failure := e.Error; failure != nil {
		w.key(&root, Keys.Error)
		object := w.object()
		w.key(&object, models.KeyCode)
		w.symbol(failure.Code())
		w.key(&object, models.KeyType)
		w.symbol(failure.Type())
		w.symbolField(&object, models.KeySource, failure.Source())
		w.key(&object, models.KeyStatus)
		w.uint(uint64(failure.Status()))
		w.key(&object, models.KeyMessage)
		w.symbol(failure.Message())
		w.end(object)
	}
	if len(e.Checkpoints) > 0 {
		w.key(&root, Keys.Checkpoints)
		w.data = append(w.data, '[')
		for i := range e.Checkpoints {
			c := &e.Checkpoints[i]
			if format == codec.CompactJSON && i > 0 {
				w.data = append(w.data, ',')
			}
			object := w.object()
			w.key(&object, 0)
			w.symbol(c.Name)
			w.key(&object, 1)
			w.integer(c.Duration.Microseconds())
			w.key(&object, 2) // checkpoint count
			w.uint(c.Count)
			w.end(object)
		}
		if format == codec.CompactJSON {
			w.data = append(w.data, ']')
		}
	}
	if e.present&(1<<Keys.ResponseBytes) != 0 {
		w.key(&root, Keys.ResponseBytes)
		w.uint(e.ResponseBytes)
	}
	w.textField(&root, Keys.Uri, e.Uri)
	w.textField(&root, Keys.ClientIp, e.ClientIp)
	if len(e.Fields) > 0 {
		w.key(&root, Keys.Fields)
		data, err := codec.MarshalCompact(e.Fields, format)
		if err != nil {
			w.err = err
		}
		w.data = append(w.data, data...)
	}
	w.textField(&root, Keys.Diagnostic, e.Diagnostic)
	if e.Dropped > 0 {
		w.key(&root, Keys.Dropped)
		w.uint(e.Dropped)
	}
	w.end(root)
	if w.err != nil {
		return nil, w.err
	}
	return w.data, nil
}
