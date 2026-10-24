package cache

// Definition-bound PIL projection into the bounded native ingestion batch.
import (
	"encoding/hex"
	"math"
	"strings"
)

func searchKind(t string, mapped bool) string {
	t = strings.TrimSuffix(strings.TrimPrefix(t, "ARRAY<"), ">")
	switch {
	case t == "STRING":
		if mapped {
			return "m"
		}
		return "s"
	case t == "UUID":
		return "e"
	case t == "BOOL":
		return "b"
	case t == "CBOR":
		return "j"
	case strings.HasPrefix(t, "TIMESTAMP_") || strings.HasPrefix(t, "I"):
		return "i"
	case strings.HasPrefix(t, "U") || t == "REF16":
		return "u"
	case t == "F32" || t == "F64":
		return "f"
	}
	return "x"
}
func searchDocument(l *Layout, row uint64, raw []byte) (searchDoc, error) {
	var frame Frame
	if e := l.Scan(raw, &frame); e != nil {
		return searchDoc{}, e
	}
	doc := searchDoc{Row: row, Cells: make([]searchCell, 0, len(frame.Values))}
	for k, f := range l.Definition.Fields {
		v := frame.Values[k]
		if k == l.Event || v.State == Absent {
			continue
		}
		kind := searchKind(f.Type, len(f.Values) > 0)

		cell := searchCell{K: k, State: 2}
		if v.State == Null {
			cell.State = 1
			doc.Cells = append(doc.Cells, cell)
			continue
		}
		typ := f.Type
		add := func(raw []byte) {
			switch kind {
			case "e":
				cell.E = append(cell.E, hex.EncodeToString(raw))
			case "m":
				if v.State == Offset {
					cell.T = append(cell.T, string(raw))
					break
				}
				n := unsigned(raw)
				cell.U = append(cell.U, n)
				cell.T = append(cell.T, f.Values[n])
			case "s":
				cell.T = append(cell.T, string(raw))
			case "u":
				cell.U = append(cell.U, unsigned(raw))
			case "i":
				n := unsigned(raw)
				shift := uint(64 - len(raw)*8)
				cell.I = append(cell.I, int64(n<<shift)>>shift)
			case "f":
				var n float64
				if typ == "F32" {
					n = float64(math.Float32frombits(order.Uint32(raw)))
				} else {
					n = math.Float64frombits(order.Uint64(raw))
				}
				if !math.IsNaN(n) && !math.IsInf(n, 0) {
					cell.F = append(cell.F, n)
				}
			case "b":
				cell.B = append(cell.B, unsigned(raw) != 0)
			case "j":
				var value any
				if decoder.Unmarshal(raw, &value) == nil {
					appendSearchText(&cell.T, value)
				}
			}
		}
		if strings.HasPrefix(typ, "ARRAY<") {
			typ = strings.TrimSuffix(strings.TrimPrefix(typ, "ARRAY<"), ">")
			at := 0
			n, e := readExtended(v.Bytes, &at)
			if e != nil {
				return doc, e
			}
			if kind == "s" || kind == "j" {
				ends := make([]uint64, n)
				for j := range ends {
					ends[j], e = readExtended(v.Bytes, &at)
					if e != nil {
						return doc, e
					}
				}
				start := uint64(0)
				for _, end := range ends {
					add(v.Bytes[at+int(start) : at+int(end)])
					start = end
				}
			} else if n > 0 {
				width := (len(v.Bytes) - at) / int(n)
				for j := 0; j < int(n); j++ {
					add(v.Bytes[at+j*width : at+(j+1)*width])
				}
			}
		} else {
			add(v.Bytes)
		}
		for _, text := range cell.T {
			cell.C = append(cell.C, strings.ToLower(text))
		}
		doc.Cells = append(doc.Cells, cell)
	}
	return doc, nil
}
func appendSearchText(out *[]string, value any) {
	switch v := value.(type) {
	case string:
		*out = append(*out, v)
	case []any:
		for _, x := range v {
			appendSearchText(out, x)
		}
	case map[any]any:
		for _, x := range v {
			appendSearchText(out, x)
		}
	case map[string]any:
		for _, x := range v {
			appendSearchText(out, x)
		}
	}
}
