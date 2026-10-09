package logging

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
	Keys "trie-trunk/go/logger/client/keys"

	"trie-trunk/go/codec"
	"trie-trunk/go/models"
)

// The general codec is an independent oracle for direct compact output. This
// covers omission, explicit zero, numeric widths, nesting and text escaping.
func FuzzDirectEntryEncoding(f *testing.F) {
	for _, seed := range [][]byte{
		{}, {0}, {23}, {24}, {255}, {0, 1}, {255, 255},
		bytes.Repeat([]byte{255}, 16), []byte("\"\\\n<>&\u2028é世界"),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 4096 {
			input = input[:4096]
		}
		var raw [16]byte
		copy(raw[:], input)
		n := binary.LittleEndian.Uint64(raw[:8])
		text := strings.ToValidUTF8(string(input), "?")
		e := Entry{Category: SUMMARY, Level: INFO, Id: codec.UUIDv7(raw),
			Duration: time.Duration(int64(n)), Service: codec.Ref(uint16(n)),
			HttpStatus: uint16(n), ResponseBytes: n, Dropped: n,
			Version: text, Uri: text, ClientId: text, ClientTransactionId: text,
			ClientIp: text, Diagnostic: text, Message: codec.Text(text),
			Operation: codec.Ref(256), Resource: codec.Ref(24), HttpMethod: codec.Ref(1),
		}
		if raw[0]&1 != 0 {
			e.present |= 1<<Keys.HttpStatus | 1<<Keys.ResponseBytes
		}
		if raw[0]&2 != 0 {
			e.Error = models.New(models.INVALID_INPUT).WithSource(codec.Text(text)).WithMessage(text)
		}
		for i := 0; i < int(raw[0]%33); i++ {
			e.Checkpoints = append(e.Checkpoints, Checkpoint{Name: codec.Ref(uint16(i + 1)), Duration: time.Duration(int64(n)), Count: n})
		}
		if raw[0]&4 != 0 {
			e.Fields = map[string]codec.Value{"text": codec.String(text), "n": codec.Uint(n), "nested": codec.List(codec.Int(-1), codec.Bool(false))}
		}
		for _, format := range []codec.Format{codec.CBOR, codec.CompactJSON} {
			got, err := e.Encode(format, nil)
			if err != nil {
				t.Fatal(err)
			}
			want, err := codec.Encode(e.Record(), BuildSchema(), nil, format)
			if err != nil {
				t.Fatal(err)
			}
			if format == codec.CBOR {
				if !bytes.Equal(got, want) {
					t.Fatalf("CBOR mismatch:\n%x\n%x", got, want)
				}
			} else {
				// JSON object key order differs; numbers must retain their exact precision.
				decode := func(b []byte) any {
					d := json.NewDecoder(bytes.NewReader(b))
					d.UseNumber()
					var v any
					if err := d.Decode(&v); err != nil {
						t.Fatal(err)
					}
					return v
				}
				if !reflect.DeepEqual(decode(got), decode(want)) {
					t.Fatalf("JSON mismatch:\n%s\n%s", got, want)
				}
			}
		}
	})
}
