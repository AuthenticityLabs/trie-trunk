package cache

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Keep the browser envelope fixture reproducible through the actual PIL writer.
func TestExportEventCompressionFixture(t *testing.T) {
	path := os.Getenv("PIL_COMPRESSION_FIXTURE")
	if path == "" {
		t.Skip("set PIL_COMPRESSION_FIXTURE to export")
	}
	d := testLayout(t, false).Definition
	d.PIL = 1
	d.AdaptiveIntegers = "16"
	l, err := Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	raw := compressionFrame(t, l, 1, 4096)
	packed, err := l.CompressEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	// Standalone transport envelope fixtures intentionally include per-event
	// compression; native storage fixtures cover complete hourly PACs separately.
	wires := map[string][]byte{}
	for _, path := range []string{"/collect", "/events", "/collect?format=native"} {
		var wire bytes.Buffer
		wire.Write([]byte{'T', 'P', 'I', 'L', 2})
		if err = writeDefine(&wire, l); err != nil {
			t.Fatal(err)
		}
		if err = writeBind(&wire, 0, l.Digest, "demo/payment/transactions.v1/write"); err != nil {
			t.Fatal(err)
		}
		wire.WriteByte(255)
		wire.Write([]byte{'T', 'P', 'I', 'L', 2})
		if err = writeBind(&wire, 0, l.Digest, "demo/payment/transactions.v1/write"); err != nil {
			t.Fatal(err)
		}
		if path == "/collect?format=native" {
			// Deliberately reject the native envelope before it can publish
			// coverage: a manifest must bind the exact Definition digest.
			manifest, e := encoder.Marshal(map[string]any{"Definition": make([]byte, 32), "RowWidth": IndexRowSize})
			if e != nil {
				t.Fatal(e)
			}
			control := nativeControl("U32", "OBJECT")
			frame, e := control.Encode(nil, []Value{Uint(0, 4), {Offset, manifest}})
			if e != nil {
				t.Fatal(e)
			}
			wire.WriteByte(4)
			wire.Write(frame)
		} else if err = writeEvent(&wire, l, 0, packed, true); err != nil {
			t.Fatal(err)
		}
		wire.Write([]byte{255, 254})
		wires[path] = wire.Bytes()
	}
	data, err := json.MarshalIndent(struct {
		Definition, Raw, Packed []byte
		Wires                   map[string][]byte
	}{l.Bytes, raw, packed, wires}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
}
