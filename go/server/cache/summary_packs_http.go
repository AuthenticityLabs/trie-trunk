package cache

// Summary searches select the exact content identities advertised by the view
// snapshot. The response is a PIL ARRAY<BYTES> of unchanged compressed daily packs.
import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
)

const summaryBatchLimit = 1024

type summaryPackQuery struct {
	Source     string `json:"source"`
	Definition string `json:"definition"`
	Key        string `json:"key"`
}

type summaryPackBody struct {
	reader io.Reader
	length uint64
}

func (h *HTTP) summariesHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	kind, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if kind != "application/json" {
		http.Error(w, "application/json search required", 415)
		return
	}
	var query struct {
		Packs []summaryPackQuery `json:"packs"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&query); err != nil {
		http.Error(w, "invalid summary search", 400)
		return
	}
	if d.Decode(new(any)) != io.EOF || query.Packs == nil || len(query.Packs) > summaryBatchLimit || r.URL.RawQuery != "" {
		http.Error(w, "summary search requires at most 1024 pack references", 400)
		return
	}
	seen := make(map[summaryPackQuery]bool)
	for _, ref := range query.Packs {
		key, ke := hex.DecodeString(ref.Key)
		def, de := hex.DecodeString(ref.Definition)
		if ke != nil || de != nil || len(key) != 32 || len(def) != 32 || ref.Source == "" || seen[ref] {
			http.Error(w, "invalid or duplicate summary reference", 400)
			return
		}
		seen[ref] = true
	}
	apis := h.sources
	if len(apis) == 0 && h.Store != nil {
		apis = []*HTTP{h}
	}
	// Pin files before sending any response bytes. Retention may replace their
	// paths, but cannot change these open inodes. Share one FD per branch/store.
	files := make(map[*branchStore]*os.File)
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	lookup := make(map[summaryPackQuery]summaryPackBody)
	for _, api := range apis {
		s := api.Store
		definition := fmtDigestNative(s.layout.Digest)
		for branch := Read; branch <= Trace; branch++ {
			source := api.tap(branch)
			var refs []summaryPackQuery
			for _, ref := range query.Packs {
				if ref.Definition == definition && ref.Source == source {
					refs = append(refs, ref)
				}
			}
			if len(refs) == 0 {
				continue
			}
			if r.Context().Err() != nil {
				return
			}
			err := func() error {
				s.mu.Lock()
				defer s.mu.Unlock()
				b, err := s.openBranch(branch)
				if err != nil {
					return err
				}
				locations := make(map[string]summaryDayLocation, len(b.summaryDays))
				for _, loc := range b.summaryDays {
					locations[loc.Key] = loc
				}
				for _, ref := range refs {
					if loc, ok := locations[ref.Key]; ok {
						f := files[b]
						if f == nil {
							f, err = os.Open(b.sum.Name())
							if err != nil {
								return err
							}
							files[b] = f
						}
						lookup[ref] = summaryPackBody{io.NewSectionReader(f, int64(loc.Offset), int64(loc.Length)), loc.Length}
					} else if raw := s.summaryExports[ref.Key]; raw != nil {
						lookup[ref] = summaryPackBody{bytes.NewReader(raw), uint64(len(raw))}
					}
				}
				return nil
			}()
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
	}
	array := extended(nil, uint64(len(query.Packs)))
	var end uint64
	for _, ref := range query.Packs {
		pack, ok := lookup[ref]
		if !ok {
			http.Error(w, "summary snapshot expired or unavailable; retry search", 409)
			return
		}
		end += pack.length
		array = extended(array, end)
	}
	prefix := extended([]byte{0xc0}, uint64(len(array))+end)
	prefix = append(prefix, array...)
	length := uint64(len(prefix)) + end
	w.Header().Set("Content-Type", "application/pil")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Length", strconv.FormatUint(length, 10))
	out := summaryContextWriter{r.Context(), w}
	if _, err := out.Write(prefix); err != nil {
		return
	}
	buf := make([]byte, 32<<10)
	for _, ref := range query.Packs {
		pack := lookup[ref]
		n, err := io.CopyBuffer(out, pack.reader, buf)
		if err != nil || uint64(n) != pack.length {
			panic(http.ErrAbortHandler)
		}
	}
}

type summaryContextWriter struct {
	ctx context.Context
	out io.Writer
}

func (w summaryContextWriter) Write(b []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.out.Write(b)
}
