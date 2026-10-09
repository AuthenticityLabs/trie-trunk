package serve

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
	"trie-trunk/go/server/search"
)

func TestGETSelectionRoundTrip(t *testing.T) {
	count := uint64(123)
	base := search.Config{Tap: "test", Start: time.Unix(1700000000, 123456789).UTC(), End: time.Unix(1700000060, 987654321).UTC(), Tail: true, Where: []string{`service=="one & two"`, `message=="a+b?=é"`}, Headers: []string{"id", "time"}}
	for _, limit := range []*uint64{nil, &count} {
		for _, headers := range [][]string{nil, {}, {"id", "time"}} {
			c := base
			c.Limit = limit
			c.Headers = headers
			var got search.Config
			if err := decodeSelection(httptest.NewRequest("GET", "/collect?"+c.Query().Encode(), nil), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c) {
				t.Fatalf("got %#v want %#v", got, c)
			}
		}
	}
}
func TestGETSelectionRejectsAmbiguityAndBodies(t *testing.T) {
	for _, query := range []string{"limit=-1", "limit=1&limit=2", "from=noon", "from=2026-01-01", "follow=yes", "where=a%xx", "root=/etc", "negotiation=x", "pretty=true"} {
		if err := decodeSelection(httptest.NewRequest("GET", "/collect?"+query, nil), &search.Config{}); err == nil {
			t.Fatal("accepted", query)
		}
	}
	if err := decodeSelection(httptest.NewRequest("GET", "/collect", strings.NewReader(`{}`)), &search.Config{}); err == nil {
		t.Fatal("accepted GET body")
	}
	limit := uint64(100)
	c := search.Config{Limit: &limit, Tail: true}
	if err := decodeSelection(httptest.NewRequest("GET", "/collect", nil), &c); err != nil || *c.Limit != 100 || !c.Tail {
		t.Fatal("defaults changed", err)
	}
}
func TestDefinitionKnownHint(t *testing.T) {
	hash := strings.Repeat("ab", 32)
	got, err := knownDefinitions([]string{strings.ToUpper(hash), hash + ", " + hash})
	if err != nil || !reflect.DeepEqual(got, []string{hash, hash, hash}) {
		t.Fatal(got, err)
	}
	for _, value := range []string{"xyz", strings.Repeat("a", 63), strings.Repeat(hash+",", 129)} {
		if _, err := knownDefinitions([]string{value}); err == nil {
			t.Fatal("accepted malformed cache hint")
		}
	}
}
