package cache

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	pil "github.com/AuthenticityLabs/pill-packer/go/codec"
)

func TestSummarySearchValidationAndAuthorization(t *testing.T) {
	h := &HTTP{}
	for _, tc := range []struct {
		method, body, token, content string
		status                       int
	}{
		{"GET", "", "", "application/json", 405},
		{"POST", `{"packs":[]}`, "", "text/plain", 415},
		{"POST", `{}`, "", "application/json", 400},
		{"POST", `{"packs":[],"extra":true}`, "", "application/json", 400},
		{"POST", `{"packs":[]} {}`, "", "application/json", 400},
		{"POST", `{"packs":[{"key":"bad"}]}`, "", "application/json", 400},
		{"POST", `{"packs":[]}`, "secret", "application/json", 401},
		{"POST", `{"packs":[]}`, "", "application/json", 200},
	} {
		h.Token = tc.token
		r := httptest.NewRequest(tc.method, "/summaries", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", tc.content)
		w := httptest.NewRecorder()
		h.serveHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
		if w.Code == 200 && !bytes.Equal(w.Body.Bytes(), []byte{0xc0, 2, 0, 0, 0}) {
			t.Fatal("empty array", w.Body.Bytes())
		}
	}
	h.Token = ""
	ref := summaryPackQuery{Source: "demo/payment/transactions.v1/read", Definition: strings.Repeat("a", 64), Key: strings.Repeat("b", 64)}
	if w := requestSummaryPacks(t, h, []summaryPackQuery{ref, ref}); w.Code != 400 {
		t.Fatal("duplicate", w.Code)
	}
	if w := requestSummaryPacks(t, h, make([]summaryPackQuery, summaryBatchLimit+1)); w.Code != 400 {
		t.Fatal("unbounded request", w.Code)
	}
	if w := requestSummaryPacks(t, h, []summaryPackQuery{ref}); w.Code != 409 || w.Header().Get("Content-Type") == "application/pil" {
		t.Fatal("missing pack must fail before array publication", w.Code)
	}
}

func TestSummaryArrayEncoding(t *testing.T) {
	for _, n := range []int{0, 1, 23, 24, 255, 256, 1024} {
		array := extended(nil, uint64(n))
		for i := 0; i < n; i++ {
			array = extended(array, uint64((i+1)*32))
		}
		wire := extended([]byte{0xc0}, uint64(len(array)+n*32))
		wire = append(wire, array...)
		wire = append(wire, make([]byte, n*32)...)
		l, err := pil.Compile(pil.Definition{Fields: []pil.Field{{Name: "packs", Type: "ARRAY<BYTES>"}}})
		if err != nil {
			t.Fatal(err)
		}
		var f pil.Frame
		if err = l.Scan(wire, &f); err != nil {
			t.Fatal(n, err)
		}
		values, err := pil.FieldValue(l.Definition.Fields[0], f.Values[0])
		if err != nil || len(values.([]any)) != n {
			t.Fatal(n, err)
		}
	}
	var query struct {
		Packs []summaryPackQuery `json:"packs"`
	}
	if err := json.Unmarshal([]byte(`{"packs":[]}`), &query); err != nil || query.Packs == nil {
		t.Fatal(err)
	}
}
