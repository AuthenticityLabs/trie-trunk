package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func replayRequest(p *Endpoint, path string, known ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("Accept", "application/pil")
	r.Header.Set("PIL-Version", "2")
	r.Header.Set("PIL-Features", "adaptive-integers")
	if len(known) > 0 {
		r.Header.Set("Definition-Known", known[0])
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	return w
}
func decodeHistory(t *testing.T, raw []byte) (frames [][]byte, digests [][32]byte) {
	t.Helper()
	if !bytes.HasPrefix(raw, []byte{'T', 'P', 'I', 'L', 2}) {
		t.Fatal("missing preamble")
	}
	r := bytes.NewReader(raw[5:])
	maps := map[[32]byte]*Layout{}
	bindings := map[uint32][32]byte{}
	for {
		tag, e := r.ReadByte()
		if e != nil {
			t.Fatal(e)
		}
		if tag == 255 {
			last, _ := r.ReadByte()
			if last != 254 || r.Len() != 0 {
				t.Fatal("incomplete history")
			}
			return
		}
		switch tag {
		case 1:
			l := nativeControl("BYTES", "BYTES", "BYTES")
			b, e := l.ReadFrame(r, nil)
			if e != nil {
				t.Fatal(e)
			}
			var f Frame
			if e = l.Scan(b, &f); e != nil {
				t.Fatal(e)
			}
			d, e := ParseDefinition(f.Values[2].Bytes)
			if e != nil {
				t.Fatal(e)
			}
			maps[d.Digest] = d
		case 2:
			l := nativeControl("U32", "BYTES", "STRING")
			b, e := l.ReadFrame(r, nil)
			if e != nil {
				t.Fatal(e)
			}
			var f Frame
			if e = l.Scan(b, &f); e != nil {
				t.Fatal(e)
			}
			var digest [32]byte
			copy(digest[:], f.Values[1].Bytes)
			bindings[uint32(unsigned(f.Values[0].Bytes))] = digest
		case 3:
			var b [4]byte
			if _, e = io.ReadFull(r, b[:]); e != nil {
				t.Fatal(e)
			}
			digest := bindings[order.Uint32(b[:])]
			l := maps[digest]
			if l == nil {
				t.Fatal("unknown map")
			}
			b2, e := l.ReadFrame(r, nil)
			if e != nil {
				t.Fatal(e)
			}
			frames = append(frames, b2)
			digests = append(digests, digest)
		default:
			t.Fatal("unexpected history tag", tag)
		}
	}
}

func TestReplayHTTPMinorHistoryAndInvalidation(t *testing.T) {
	base := testLayout(t, false)
	d := base.Definition
	d.Version++
	d.Resource.Minor++
	next, e := Compile(d)
	if e != nil {
		t.Fatal(e)
	}
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	now := at.Add(2 * time.Hour)
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: base, ObjectIndex: true, Now: func() time.Time { return now }}
	p, e := OpenEndpoint(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { p.Close() }()
	old := p.api.Store
	frame1 := testFrame(t, base, at, 1, false)
	if _, e = old.Append(Write, frame1); e != nil {
		t.Fatal(e)
	}
	if e = p.Prefer(next); e != nil {
		t.Fatal(e)
	}
	current := p.api.Store
	frame2 := testFrame(t, next, at.Add(time.Minute), 2, true)
	if _, e = current.Append(Write, frame2); e != nil {
		t.Fatal(e)
	}
	query := "collection=payment%2Ftransactions.v1&id=00000000-0000-7000-8000-00000000002a"
	path := "/objects/history?" + query + "&from=2026-10-16T00:00:00Z&to=2026-10-17T00:00:00Z"
	w := replayRequest(p, path)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	frames, digests := decodeHistory(t, w.Body.Bytes())
	if len(frames) != 2 || !bytes.Equal(frames[0], frame1) || !bytes.Equal(frames[1], frame2) || digests[0] != base.Digest || digests[1] != next.Digest {
		t.Fatal("historical definition or frame changed")
	}
	observed := w.Header().Get("Trunk-State")
	warm := replayRequest(p, path, fmt.Sprintf("%x,%x", base.Digest, next.Digest))
	if warm.Code != 200 || warm.Body.Len() >= w.Body.Len() || !bytes.Contains(warm.Body.Bytes(), frame1) || !bytes.Contains(warm.Body.Bytes(), frame2) {
		t.Fatal("known maps were not reused", warm.Code)
	}
	if dir := os.Getenv("PIL_HISTORY_FIXTURE_DIR"); dir != "" {
		if e = os.MkdirAll(dir, 0755); e != nil {
			t.Fatal(e)
		}
		meta, _ := json.Marshal(map[string]string{"token": observed, "definition": w.Header().Get("Definition-Version")})
		for name, raw := range map[string][]byte{"history.pil": w.Body.Bytes(), "warm.pil": warm.Body.Bytes(), "metadata.json": meta} {
			if e = os.WriteFile(filepath.Join(dir, name), raw, 0644); e != nil {
				t.Fatal(e)
			}
		}
	}
	if len(observed) != 64 {
		t.Fatal("missing state token")
	}
	check := func(token string, id string) bool {
		t.Helper()
		w := replayRequest(p, "/objects/state?collection=payment%2Ftransactions.v1&id="+id+"&token="+token)
		var v struct {
			Token   string
			Current bool
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		return v.Current
	}
	if !check(observed, "00000000-0000-7000-8000-00000000002a") {
		t.Fatal("unchanged state rejected")
	}
	other, e := current.Token(testObject(43))
	if e != nil {
		t.Fatal(e)
	}
	// This error stays in the durable staging tail. Replay invalidates immediately,
	// without waiting for error payload finalization or an error subscription.
	if _, e = current.Append(Error, testFrame(t, next, at.Add(2*time.Hour), 3, false)); e != nil {
		t.Fatal(e)
	}
	if check(observed, "00000000-0000-7000-8000-00000000002a") {
		t.Fatal("error did not invalidate object")
	}
	if !check(stateTokenText(other), "00000000-0000-7000-8000-00000000002b") {
		t.Fatal("unrelated object invalidated")
	}
	current.policy.Retention.Error = "1s"
	now = at.Add(3 * time.Hour)
	if e = current.ApplyRetention(); e != nil {
		t.Fatal(e)
	}
	if check(observed, "00000000-0000-7000-8000-00000000002a") {
		t.Fatal("retention cleared uncertainty")
	}
	if len(current.branches[Error].rows) != 0 {
		t.Fatal("error payload did not expire")
	}
	if e = p.Close(); e != nil {
		t.Fatal(e)
	}
	cfg.Layout = nil
	p, e = OpenEndpoint(cfg)
	if e != nil {
		t.Fatal(e)
	}
	if check(observed, "00000000-0000-7000-8000-00000000002a") {
		t.Fatal("restart lost state")
	}
	w = replayRequest(p, path)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	frames, _ = decodeHistory(t, w.Body.Bytes())
	if len(frames) != 2 {
		t.Fatal("restart lost cross-minor history")
	}
	for _, bad := range []string{"/objects/state?collection=payment%2Ftransactions.v1&id=00000000-0000-7000-8000-00000000002a&token=bad", "/objects/state?" + query + "&id=43", "/objects/history?" + query, "/objects/state?collection=payment%2Ftransactions.v1&id=18446744073709551616"} {
		if w = replayRequest(p, bad); w.Code != 400 {
			t.Fatal(bad, w.Code)
		}
	}
	r := httptest.NewRequest("GET", path, nil)
	w = httptest.NewRecorder()
	p.Handler("secret").ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("history bypassed auth")
	}
}

func TestReplayRequiresObjectIndexAndIdentityTextIsLossless(t *testing.T) {
	l := testLayout(t, false)
	s, e := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	w := replayRequest(s, "/objects/history?collection=payment%2Ftransactions.v1&id=00000000-0000-7000-8000-00000000002a&from=2026-10-16T00:00:00Z&to=2026-10-17T00:00:00Z")
	if w.Code != 409 {
		t.Fatal("disabled collection.idx did not reject", w.Code)
	}
	v, e := objectText(l, "ffffffff-ffff-4fff-bfff-ffffffffffff")
	if e != nil || len(v.Bytes) != 16 || v.Bytes[15] != 255 {
		t.Fatal(v, e)
	}
	for _, kind := range []string{"STRING", "BYTES", "UUID"} {
		d := l.Definition
		d.Fields = append([]Field(nil), d.Fields...)
		d.Fields[1].Type = kind
		layout, e := Compile(d)
		if e != nil {
			t.Fatal(e)
		}
		text := map[string]string{"STRING": "customer/ID ?✓", "BYTES": "AP_-", "UUID": "00112233-4455-6677-8899-aabbccddeeff"}[kind]
		value, e := objectText(layout, text)
		if e != nil || len(value.Bytes) == 0 {
			t.Fatal(kind, e)
		}
		if kind == "STRING" && string(value.Bytes) != text {
			t.Fatal("identity changed")
		}
		if kind == "UUID" && (len(value.Bytes) != 16 || value.Bytes[0] != 0 || value.Bytes[15] != 255) {
			t.Fatal("UUID byte order")
		}
	}
}

func TestHistorySnapshotAllowsWriterRetentionAndClose(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	p, e := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, ObjectIndex: true, Now: func() time.Time { return at.Add(2 * time.Hour) }})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	s := p.api.Store
	expected := [][]byte{testFrame(t, l, at, 1, false), testFrame(t, l, at.Add(time.Minute), 2, true)}
	for _, raw := range expected {
		if _, e = s.Append(Write, raw); e != nil {
			t.Fatal(e)
		}
	}
	s.policy.Retention.Write = "1h"
	ready, release := make(chan EventReference, 1), make(chan struct{})
	done := make(chan error, 1)
	var frames [][]byte
	go func() {
		done <- p.History(context.Background(), l.Definition.Identity, 1, testObject(42), at, at.Add(time.Hour), func(ref EventReference, b []byte) error {
			frames = append(frames, append([]byte(nil), b...))
			if len(frames) == 1 {
				ready <- ref
				<-release
			}
			return nil
		})
	}()
	ref := <-ready
	work := make(chan error, 1)
	go func() {
		_, err := s.Append(Write, testFrame(t, l, at.Add(2*time.Hour), 3, false))
		if err == nil {
			err = s.ApplyRetention()
		}
		work <- err
	}()
	select {
	case e = <-work:
		if e != nil {
			close(release)
			<-done
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		close(release)
		<-done
		t.Fatal("history held writer lock")
	}
	if _, e = p.Resolve(ref); e == nil {
		close(release)
		<-done
		t.Fatal("retired generation reference resolved")
	}
	if e = p.Close(); e != nil {
		close(release)
		<-done
		t.Fatal(e)
	}
	close(release)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if len(frames) != 2 || !bytes.Equal(frames[0], expected[0]) || !bytes.Equal(frames[1], expected[1]) {
		t.Fatal("snapshot changed")
	}
}
