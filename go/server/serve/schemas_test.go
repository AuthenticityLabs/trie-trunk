package serve

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"trie-trunk/go/codec"
	"trie-trunk/go/server/search"
	"trie-trunk/go/server/store"
)

func requestSchemas(t *testing.T, h *Handler, request schemaRequest) schemaResponse {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/schemas", bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var response schemaResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestSchemaNegotiationRequiredAndCached(t *testing.T) {
	selection, writer := events(t)
	h, err := New(selection.Root, "", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	h.run = func(_ context.Context, config search.Config, _ io.Writer) error {
		calls++
		if !config.Compact {
			t.Fatal("expected compact output")
		}
		return nil
	}
	pull := func(token string, status int) {
		t.Helper()
		selection.Negotiation = token
		data, err := json.Marshal(selection)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/events", bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", search.DictionaryJSONL)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	pull("", http.StatusPreconditionRequired)
	first := requestSchemas(t, h, schemaRequest{Tap: "test"})
	if len(first.Schemas) != 1 || first.Schemas[0].Dictionary == nil || first.Schemas[0].Version != 42 || len(first.Negotiation) != 64 {
		t.Fatal(first)
	}
	catalogs, err := store.ReadCatalogs(context.Background(), selection.Root, "test")
	if err != nil || len(catalogs) != 1 {
		t.Fatal(catalogs, err)
	}
	if first.Schemas[0].Digest != hex.EncodeToString(catalogs[0].Revision.Digest[:]) {
		t.Fatal("signature differs from stored CBOR")
	}
	cached := requestSchemas(t, h, schemaRequest{Tap: "test", Known: []string{first.Schemas[0].Digest}})
	if cached.Negotiation != first.Negotiation || len(cached.Schemas) != 1 || cached.Schemas[0].Dictionary != nil {
		t.Fatal(cached)
	}
	pull(strings.Repeat("0", 64), http.StatusConflict)
	pull(first.Negotiation, 200)
	if calls != 1 {
		t.Fatal("unnegotiated search executed", calls)
	}
	definition := *first.Schemas[0].Dictionary
	definition.Version++
	revision, data, err := definition.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Install(revision, data); err != nil {
		t.Fatal(err)
	}
	pull(first.Negotiation, http.StatusConflict)
	updated := requestSchemas(t, h, schemaRequest{Tap: "test", Known: []string{first.Schemas[0].Digest}})
	if len(updated.Schemas) != 2 || updated.Negotiation == first.Negotiation || updated.Schemas[0].Dictionary != nil || updated.Schemas[1].Dictionary == nil {
		t.Fatal(updated)
	}
	pull(updated.Negotiation, 200)
	selection.Tap = ""
	pull(updated.Negotiation, http.StatusConflict)
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestSchemaNegotiationTapScopeAndBusyFollowers(t *testing.T) {
	selection, _ := events(t)
	other, err := store.Open(store.Config{Root: selection.Root, Tap: "other"})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	definition := codec.Definition{Version: 42, Timezone: "UTC", Aggregations: []string{"count", "duration.count", "duration.sum", "duration.min", "duration.max"}, Fields: codec.Schema{8: {Name: "id", Encoding: "uuidv7"}, 12: {Name: "duration", Unit: "us"}, 3: {Name: "level", Enums: map[uint16]string{5: "FAILURE"}}}}
	rev, data, err := definition.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Install(rev, data); err != nil {
		t.Fatal(err)
	}
	h, err := New(selection.Root, "", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	h.slots <- struct{}{}
	defer func() { <-h.slots }()
	response := requestSchemas(t, h, schemaRequest{})
	if len(response.Schemas) != 2 || response.Schemas[0].Tap != "other" || response.Schemas[1].Tap != "test" || response.Schemas[0].Version != response.Schemas[1].Version || response.Schemas[0].Digest == response.Schemas[1].Digest {
		t.Fatal(response)
	}
	selected := requestSchemas(t, h, schemaRequest{Tap: "other"})
	if len(selected.Schemas) != 1 || selected.Schemas[0].Tap != "other" || selected.Negotiation == response.Negotiation {
		t.Fatal(selected)
	}
}

func TestSchemaNegotiationValidationAndAuthentication(t *testing.T) {
	h, err := New(t.TempDir(), "secret", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		body, auth string
		status     int
	}{
		{`{}`, "", 401},
		{`{"tap":"../escape"}`, "secret", 400},
		{`{"known":["wrong"]}`, "secret", 400},
		{`{"unknown":true}`, "secret", 400},
		{`{} {}`, "secret", 400},
		{strings.Repeat(" ", 65537) + `{}`, "secret", 400},
		{`{}`, "secret", 200},
	} {
		r := httptest.NewRequest(http.MethodPost, "/schemas", strings.NewReader(test.body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+test.auth)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatal(w.Code, w.Body.String(), test.status)
		}
	}
}
