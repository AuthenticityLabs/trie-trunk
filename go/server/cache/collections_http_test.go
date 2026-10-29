package cache

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestCollectionHistoryPathFetchesLatestRetainedAndLiveWrites(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	p, err := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, ObjectIndex: true, Now: func() time.Time { return at.Add(2 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	s := p.api.Store
	first := testFrame(t, l, at, 1, false)
	if _, err = s.Append(Write, first); err != nil {
		t.Fatal(err)
	}
	if err = s.Flush(); err != nil {
		t.Fatal(err)
	}
	path := "/collections/00000000-0000-7000-8000-00000000002a?cache=demo&collection=payment%2Ftransactions.v1"
	get := func() [][]byte {
		w := replayRequest(p, path)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("history must query the origin")
		}
		frames, _ := decodeHistory(t, w.Body.Bytes())
		return frames
	}
	if frames := get(); len(frames) != 1 || !sameSeedData(t, l, frames[0], first) {
		t.Fatal("missing retained write")
	}
	latest := testFrame(t, l, at.Add(90*time.Minute), 2, true)
	if _, err = s.Append(Write, latest); err != nil {
		t.Fatal(err)
	}
	if frames := get(); len(frames) != 2 || !sameSeedData(t, l, frames[1], latest) {
		t.Fatal("fresh lookup omitted live write")
	}
	if w := replayRequest(p, path+"&id=43"); w.Code != 400 {
		t.Fatal("accepted a conflicting query identity", w.Code)
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
	if w.Code != 405 {
		t.Fatal("accepted a history write", w.Code)
	}
}
