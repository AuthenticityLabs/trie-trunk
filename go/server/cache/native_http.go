package cache

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Native range delivery is explicitly negotiated on the same Collect GET.
// TPIL/2 tags 4/5/6 carry a manifest, native chunk, and completed package. They
// are storage replication controls, not a PIL PACK or a new event encoding.
// Collection always delivers whole canonical PACs; /events plans selected rows.
func (h *HTTP) nativeCollect(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" || r.URL.Path != "/collect" {
		http.Error(w, "native ranges require Collect GET", 405)
		return
	}
	if !strings.EqualFold(r.Header.Get("Accept"), "application/pil") || r.Header.Get("PIL-Version") != "2" || !strings.Contains(","+r.Header.Get("PIL-Features")+",", ",native-ranges,") {
		http.Error(w, "PIL/2 native-ranges capability required", 426)
		return
	}

	q := r.URL.Query()
	if q.Get("format") != "" && q.Get("format") != "native" {
		http.Error(w, "collection requires native PAC representation", 400)
		return
	}
	for key := range q {
		switch key {
		case "format", "tap", "from", "to", "definition", "population":
		default:
			http.Error(w, "unsupported native selection parameter", 400)
			return
		}
	}
	from, e := time.Parse(time.RFC3339Nano, q.Get("from"))
	if e != nil {
		http.Error(w, "invalid from", 400)
		return
	}
	to, e := time.Parse(time.RFC3339Nano, q.Get("to"))
	if e != nil || !from.Before(to) {
		http.Error(w, "invalid time window", 400)
		return
	}
	if q.Get("population") != "" && q.Get("population") != "none" {
		http.Error(w, "population must be omitted or none", 400)
		return
	}
	// Coverage is scoped by immutable definition + physical generation. Bound the
	// advertisement; a client may send fewer spans safely (at worst re-fetching).
	var have []Coverage
	raw := r.Header.Get("Trunk-Coverage")
	if len(raw) > 16384 {
		http.Error(w, "coverage advertisement exceeds 16 KiB", 431)
		return
	}
	if raw != "" {
		body, e := base64.RawURLEncoding.DecodeString(raw)
		if e != nil {
			http.Error(w, "invalid coverage", 400)
			return
		}
		if e = json.Unmarshal(body, &have); e != nil || len(have) > 128 {
			http.Error(w, "invalid coverage", 400)
			return
		}
	}
	sources := h.sources
	if len(sources) == 0 {
		sources = []*HTTP{h}
	}
	type selected struct {
		api     *HTTP
		branch  Branch
		stream  uint32
		windows []PackageWindow
		have    Coverage
	}
	var targets []selected
	for _, api := range sources {
		l := api.Store.layout
		if digest := q.Get("definition"); digest != "" && digest != fmtDigestNative(l.Digest) {
			continue
		}
		branches, e := api.selectedBranches(q.Get("tap"))
		if e != nil {
			continue
		}
		if l.integers != nil && !hasAdaptive(r) {
			http.Error(w, "adaptive-integers capability required", 426)
			return
		}
		for _, b := range branches {
			windows, e := api.Store.PlanBranchPackages(b, from, to)
			if e != nil {
				http.Error(w, e.Error(), 400)
				return
			}
			// Coverage for two branches can share a definition but never a generation.
			generation := api.Store.Generation(b)
			known := Coverage{}
			for _, v := range have {
				if v.Definition == l.Digest && (fmt.Sprintf("%x", v.Generation) == generation || generation == "") {
					known = v
					break
				}
			}
			targets = append(targets, selected{api, b, uint32(len(targets)), windows, known})
		}
	}
	if len(targets) == 0 {
		http.Error(w, "unknown collection source", 404)
		return
	}
	w.Header().Set("Content-Type", "application/pil")
	w.Header().Set("PIL-Version", "2")
	version := fmtDigestNative(targets[0].api.Store.layout.Digest)
	if len(sources) > 1 {
		hash := sha256.New()
		for _, target := range targets {
			hash.Write(target.api.Store.layout.Digest[:])
			hash.Write([]byte{byte(target.branch)})
		}
		version = fmt.Sprintf("%x", hash.Sum(nil))
	}
	w.Header().Set("Definition-Version", version)
	w.Header().Set("Trunk-Format", "native-pac/0")
	w.Header().Set("Trunk-Completion", "body")
	w.Header().Set("Trailer", "Trunk-Status")
	w.Header().Set("Vary", "Accept, Accept-Encoding, PIL-Version, PIL-Features, Definition-Version, Definition-Known, Trunk-Coverage")
	buffer := bufio.NewWriterSize(w, 128<<10)
	out := buffer
	_, e = out.Write([]byte{'T', 'P', 'I', 'L', 2})
	defined := map[[32]byte]bool{}
	knownMaps := "," + r.Header.Get("Definition-Known") + ","
	for _, target := range targets {
		if e != nil {
			break
		}
		l := target.api.Store.layout
		if !defined[l.Digest] && !strings.Contains(knownMaps, ","+fmtDigestNative(l.Digest)+",") {
			e = writeDefine(out, l)
		}
		defined[l.Digest] = true
		if e == nil {
			e = writeBind(out, target.stream, l.Digest, target.api.tap(target.branch))
		}
		coverage := target.have
		// DEFINE above supplies the map even when the caller did not cache it.
		coverage.Definition = l.Digest
		for _, window := range target.windows {
			if e != nil {
				break
			}
			if e = r.Context().Err(); e != nil {
				break
			}
			req := CollectionRequest{Branch: target.branch, At: window.From, IndexPeriod: window.IndexPeriod, Have: coverage,
				StatePages: strings.Contains(","+r.Header.Get("PIL-Features")+",", ",native-state-pages,")}
			if q.Get("population") != "none" {
				req.PopulationFrom = from
				if req.PopulationFrom.Before(window.From) {
					req.PopulationFrom = window.From
				}
				req.PopulationTo = to
				if req.PopulationTo.After(window.To) {
					req.PopulationTo = window.To
				}
			}
			var manifest Manifest
			e = target.api.Store.collectBorrowed(r.Context(), req, func(m Manifest) error {
				manifest = m
				raw, err := encoder.Marshal(m)
				if err != nil {
					return err
				}
				// U32 stream, CBOR manifest: DIRECT/OFFSET mask.
				b := []byte{4, 0xb0}
				b = order.AppendUint32(b, target.stream)
				b = extended(b, uint64(len(raw)))
				b = append(b, raw...)
				_, err = out.Write(b)
				return err
			}, func(chunk NativeChunk) error {
				if chunk.Kind != "pac" {
					return writeNativeChunk(out, target.stream, chunk)
				}

				if _, err := out.Write([]byte{7}); err != nil {
					return err
				}
				_, err := out.Write(chunk.Data)
				return err
			})
			if e != nil {
				break
			}
			end := []byte{6, 0x80}
			end = order.AppendUint32(end, target.stream)
			_, e = out.Write(end)
			if coverage.Generation != manifest.Generation {
				coverage.Index = nil
				coverage.Population = nil
			}
			coverage.Generation = manifest.Generation
			coverage.StateKnown, coverage.StateEpoch, coverage.StateSequence = true, manifest.StateEpoch, manifest.StateSequence
			coverage.TrieNodes = manifest.TrieNodes
			coverage.Index, _ = Normalize(append(coverage.Index, manifest.Index...))
			coverage.Population, _ = Normalize(append(coverage.Population, manifest.Population...))
		}
	}
	if e == nil {
		_, e = out.Write([]byte{255, 254})
	}
	if e == nil {
		e = buffer.Flush()
	}
	if e != nil {
		w.Header().Set("Trunk-Status", "error")
	} else {
		w.Header().Set("Trunk-Status", "ok")
	}
}
func fmtDigestNative(d [32]byte) string { return hex.EncodeToString(d[:]) }
func writeNativeChunk(out io.Writer, stream uint32, c NativeChunk) error {
	kind := map[string]byte{"map": 0, "trie": 1, "state": 2, "index": 3, "population": 4}[c.Kind]
	// stream U32, kind U8, source start U64, digest BYTES, data BYTES.
	// Three DIRECT + two OFFSET states occupy two mask bytes.
	b := []byte{5, 0xab, 0xc0}
	b = order.AppendUint32(b, stream)
	b = append(b, kind)
	b = order.AppendUint64(b, c.Start)
	b = extended(b, 32)
	b = extended(b, uint64(32+len(c.Data)))
	b = append(b, c.Digest[:]...)
	if _, e := out.Write(b); e != nil {
		return e
	}
	_, e := out.Write(c.Data)
	return e
}
