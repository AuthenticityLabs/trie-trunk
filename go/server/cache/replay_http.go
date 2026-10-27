package cache

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Tokens are opaque to clients. They bind a successful origin refresh to the
// collection epoch and both scope/object invalidation watermarks. Comparing a
// token never clears uncertainty for another client or object.
func stateTokenText(token StateToken) string {
	raw := append([]byte(nil), token.Epoch[:]...)
	raw = order.AppendUint64(raw, token.Collection)
	raw = order.AppendUint64(raw, token.Object)
	return hex.EncodeToString(raw)
}
func parseStateToken(raw string) (StateToken, error) {
	var token StateToken
	b, e := hex.DecodeString(raw)
	if e != nil || len(b) != 32 {
		return token, fmt.Errorf("invalid state token")
	}
	copy(token.Epoch[:], b[:16])
	token.Collection = order.Uint64(b[16:24])
	token.Object = order.Uint64(b[24:])
	return token, nil
}

// Identity text follows the declared role: decimal unsigned integer, standard
// UUID, literal UTF-8 string, or unpadded base64url bytes. No JSON number passes
// through float64, so 64-bit identities retain every bit.
func objectText(l *Layout, text string) (Value, error) {
	if l.Object < 0 {
		return Value{}, fmt.Errorf("collection has no object identity")
	}
	field := l.Definition.Fields[l.Object]
	switch field.Type {
	case "U8", "U16", "U32", "U64":
		n, e := strconv.ParseUint(text, 10, l.Widths[l.Object]*8)
		if e != nil {
			return Value{}, fmt.Errorf("invalid %s identity", field.Type)
		}
		return Uint(n, l.Widths[l.Object]), nil
	case "UUID":
		if len(text) != 36 || text[8] != '-' || text[13] != '-' || text[18] != '-' || text[23] != '-' {
			return Value{}, fmt.Errorf("invalid UUID identity")
		}
		raw, e := hex.DecodeString(strings.ReplaceAll(text, "-", ""))
		if e != nil {
			return Value{}, fmt.Errorf("invalid UUID identity")
		}
		return Value{Direct, raw}, nil
	case "STRING":
		if !utf8.ValidString(text) {
			return Value{}, fmt.Errorf("invalid UTF-8 identity")
		}
		return Text(text), nil
	case "BYTES":
		raw, e := base64.RawURLEncoding.DecodeString(text)
		if e != nil {
			return Value{}, fmt.Errorf("invalid base64url identity")
		}
		return Value{Offset, raw}, nil
	}
	return Value{}, fmt.Errorf("unsupported identity type")
}

func (p *Endpoint) replayHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET required", 405)
		return
	}
	q := r.URL.Query()
	for key := range q {
		if key != "cache" && key != "collection" && key != "id" && !(r.URL.Path == "/objects/state" && key == "token") && !(r.URL.Path == "/objects/history" && (key == "from" || key == "to")) {
			http.Error(w, "unsupported object selection parameter", 400)
			return
		}
		if len(q[key]) != 1 {
			http.Error(w, "duplicate object selection parameter", 400)
			return
		}
	}
	if !q.Has("id") {
		http.Error(w, "object id required", 400)
		return
	}
	if q.Has("cache") && q.Get("cache") != p.config.Cache {
		http.Error(w, "unknown cache", 404)
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		http.Error(w, "endpoint closed", 503)
		return
	}
	api := p.preferred[q.Get("collection")]
	p.mu.Unlock()
	if api == nil {
		http.Error(w, "unknown collection major", 404)
		return
	}
	l := api.Store.layout
	id, err := objectText(l, q.Get("id"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if r.URL.Path == "/objects/state" {
		token, err := api.Store.Token(id)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		reply := struct {
			Token   string `json:"token"`
			Current *bool  `json:"current,omitempty"`
		}{Token: stateTokenText(token)}
		if q.Has("token") {
			before, err := parseStateToken(q.Get("token"))
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			current := before == token
			reply.Current = &current
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
		return
	}
	if !strings.EqualFold(r.Header.Get("Accept"), "application/pil") {
		http.Error(w, "Accept: application/pil required", 406)
		return
	}
	if r.Header.Get("PIL-Version") != "2" {
		http.Error(w, "PIL-Version: 2 required", 426)
		return
	}
	from, err := time.Parse(time.RFC3339Nano, q.Get("from"))
	to, endErr := time.Parse(time.RFC3339Nano, q.Get("to"))
	if err != nil || endErr != nil || !from.Before(to) {
		http.Error(w, "invalid history time window", 400)
		return
	}
	history, err := p.snapshotHistory(l.Definition.Identity, l.Definition.Resource.Major, id, from, to)
	if err != nil {
		status := 400
		if errors.Is(err, ErrObjectIndexDisabled) {
			status = 409
		}
		http.Error(w, err.Error(), status)
		return
	}
	defer history.close()
	layouts := []*Layout{}
	for _, s := range history.sources {
		layouts = append(layouts, s.layout)
	}
	if len(layouts) == 0 {
		layouts = append(layouts, l)
	}
	for _, layout := range layouts {
		if layout.Adaptive() && !hasAdaptive(r) {
			http.Error(w, "adaptive-integers feature required", 426)
			return
		}
	}
	hash := sha256.New()
	streams := map[[32]byte]uint32{}
	for i, layout := range layouts {
		hash.Write(layout.Digest[:])
		streams[layout.Digest] = uint32(i)
	}
	w.Header().Set("Content-Type", "application/pil")
	w.Header().Set("PIL-Version", "2")
	w.Header().Set("Definition-Version", fmt.Sprintf("%x", hash.Sum(nil)))
	w.Header().Set("Trunk-State", stateTokenText(history.token))
	w.Header().Set("Trunk-Replay", "retained-history")
	w.Header().Set("Trunk-Completion", "body")
	w.Header().Set("Trailer", "Trunk-Status")
	w.Header().Set("Vary", "Accept, PIL-Version, PIL-Features, Definition-Known")
	out := bufio.NewWriterSize(w, 128<<10)
	_, err = out.Write([]byte{'T', 'P', 'I', 'L', 2})
	known := "," + r.Header.Get("Definition-Known") + ","
	for i, layout := range layouts {
		if err != nil {
			break
		}
		if !strings.Contains(known, ","+fmtDigestNative(layout.Digest)+",") {
			err = writeDefine(out, layout)
		}
		if err == nil {
			n := layout.Definition.Resource
			tap := p.config.Cache + "/" + n.Service + "/" + fmt.Sprintf("%s.v%d/write", n.Resource, n.Major)
			err = writeBind(out, uint32(i), layout.Digest, tap)
		}
	}
	if err == nil {
		err = history.visitBorrowed(r.Context(), func(ref EventReference, raw []byte) error {
			stream := streams[ref.Definition]
			return writeEvent(out, layouts[stream], stream, raw, hasCompression(r))
		})
	}
	if err == nil {
		_, err = out.Write([]byte{255, 254})
	}
	if err == nil {
		err = out.Flush()
	}
	if err != nil {
		w.Header().Set("Trunk-Status", "error")
	} else {
		w.Header().Set("Trunk-Status", "ok")
	}
}
