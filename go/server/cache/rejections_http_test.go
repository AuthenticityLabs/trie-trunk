package cache

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func rejectionFixture(t *testing.T) (*Store, time.Time) {
	t.Helper()
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	l := testLayout(t, false)
	s, err := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(4 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, branch := range []Branch{Write, Error} {
		if _, err = s.Append(branch, testFrame(t, l, at, 1, false)); err != nil {
			t.Fatal(err)
		}
		if err = s.Flush(); err != nil {
			t.Fatal(err)
		}
		var frame Frame
		if err = l.Scan(testFrame(t, l, at.Add(time.Minute), 2, false), &frame); err != nil {
			t.Fatal(err)
		}
		frame.Values[6] = Uint(^uint64(0), 8)
		raw, err := l.Encode(nil, frame.Values)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Append(branch, raw); err == nil {
			t.Fatal("late event accepted")
		}
	}
	return s, at
}
func TestRejectionInspectionScopeCompletionAndExactIntegers(t *testing.T) {
	s, _ := rejectionFixture(t)
	h := &HTTP{Store: s}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/rejections?format=inspection&tap=demo/payment/transactions.v1/write", nil))
	if w.Code != 200 || w.Header().Get("Trunk-Completion") != "body" {
		t.Fatal(w.Code, w.Body.String())
	}
	lines := bytes.Split(bytes.TrimSpace(w.Body.Bytes()), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatal("expected one event and completion", len(lines))
	}
	var row struct {
		Source string `json:"source"`
		Event  string `json:"event_json"`
	}
	if err := json.Unmarshal(lines[0], &row); err != nil {
		t.Fatal(err)
	}
	if row.Source != "demo/payment/transactions.v1/write" || !strings.Contains(row.Event, "18446744073709551615") || bytes.Contains(lines[0], []byte("pil_base64")) {
		t.Fatal(string(lines[0]))
	}
	var end struct {
		Complete bool `json:"complete"`
		Count    int  `json:"count"`
	}
	if err := json.Unmarshal(lines[1], &end); err != nil || !end.Complete || end.Count != 1 {
		t.Fatal(end, err)
	}
	if dir := os.Getenv("PIL_REJECTION_FIXTURE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "inspection.jsonl"), w.Body.Bytes(), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{"format=wrong", "format=inspection&where=anything", "format=inspection&tap=other/payment/transactions.v1/write"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/rejections?"+query, nil))
		if w.Code < 400 {
			t.Fatal("invalid selection accepted", query)
		}
	}
}

func TestRejectionSnapshotDoesNotBlockWriterAndExcludesLaterAppends(t *testing.T) {
	s, at := rejectionFixture(t)
	ready, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	count := 0
	go func() {
		done <- s.Rejections(func(Rejection) error {
			count++
			if count == 1 {
				close(ready)
				<-release
			}
			return nil
		})
	}()
	<-ready
	writes := make(chan error, 1)
	go func() {
		_, err := s.Append(Write, testFrame(t, s.layout, at.Add(4*time.Hour), 3, false))
		if err == nil {
			_, late := s.Append(Write, testFrame(t, s.layout, at.Add(2*time.Minute), 4, false))
			var rejection *LateEventError
			if !errors.As(late, &rejection) {
				err = fmt.Errorf("expected durable rejection, got %v", late)
			}
		}
		if err == nil {
			err = s.Close()
		}
		writes <- err
	}()
	select {
	case err := <-writes:
		if err != nil {
			close(release)
			<-done
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		<-done
		t.Fatal("audit callback held writer lock")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatal("snapshot included appended audit", count)
	}
}

func TestRejectionAvailabilityWithoutPayloadExpansion(t *testing.T) {
	s, _ := rejectionFixture(t)
	h := &HTTP{Store: s, Token: "secret"}
	request := func(query, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/rejections?"+query, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request("format=availability", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := request("format=availability", "secret")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"available":true}` {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request("format=availability&tap=demo", "secret"); w.Code != 400 {
		t.Fatal(w.Code)
	}
	empty, err := Open(Config{Root: t.TempDir(), Cache: "empty", Tenant: "test", Layout: testLayout(t, false)})
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	h.Store = empty
	w = request("format=availability", "secret")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"available":false}` {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(empty.dir, "lost.log")); !os.IsNotExist(err) {
		t.Fatal("presence check created audit", err)
	}
	// A file header created by an inspection is not a lost event.
	if err := empty.Rejections(func(Rejection) error { return nil }); err != nil {
		t.Fatal(err)
	}
	w = request("format=availability", "secret")
	if strings.TrimSpace(w.Body.String()) != `{"available":false}` {
		t.Fatal(w.Body.String())
	}
}
