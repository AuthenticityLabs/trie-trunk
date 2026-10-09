package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
	"trie-trunk/go/server/search"
	"trie-trunk/go/server/store"
)

func TestPILWireOverHTTP2AndHTTP3(t *testing.T) {
	selection, _ := events(t)
	serverTLS, clientTLS := credentials(t)
	address, _ := start(t, Config{Root: selection.Root, Address: "127.0.0.1:0", Socket: search.SocketPath(selection.Root), TLS: serverTLS, Token: "test-token"})
	var expected bytes.Buffer
	if err := search.Run(context.Background(), selection, &expected); err != nil {
		t.Fatal(err)
	}
	for _, protocol := range []string{"http2", "http3"} {
		t.Run(protocol, func(t *testing.T) {
			var tr http.RoundTripper
			major := 2
			if protocol == "http3" {
				h := &http3.Transport{TLSClientConfig: clientTLS}
				tr = h
				major = 3
				defer h.Close()
			} else {
				h := &http.Transport{TLSClientConfig: clientTLS, ForceAttemptHTTP2: true}
				tr = h
				defer h.CloseIdleConnections()
			}
			client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
			pull := func(path, accept string, body []byte) ([]byte, *http.Response) {
				req, _ := http.NewRequest("POST", "https://"+address+path, bytes.NewReader(body))
				req.Header.Set("Accept", accept)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer test-token")
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil || resp.StatusCode != 200 || resp.ProtoMajor != major || resp.Header.Get("Content-Type") != accept || resp.Trailer.Get("Trunk-Status") != "ok" {
					t.Fatalf("%s: %s %s %v %v", path, resp.Status, resp.Proto, resp.Trailer, err)
				}
				if !bytes.HasPrefix(data, []byte("TPIL\x01")) {
					t.Fatalf("expected binary PIL: %x", data)
				}
				return data, resp
			}
			definitions, negotiated := pull("/schemas", search.PILSchemas, []byte(`{"tap":"test"}`))
			query := selection
			query.Negotiation = negotiated.Header.Get("Trunk-Negotiation")
			body, _ := json.Marshal(query)
			wire, _ := pull("/events", search.PIL, body)
			var rendered bytes.Buffer
			if err := search.DecodePIL(context.Background(), selection, io.NopCloser(bytes.NewReader(append(definitions, wire...))), &rendered); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(rendered.Bytes(), expected.Bytes()) {
				t.Fatal("PIL differed from local output")
			}
			rendered.Reset()
			if err := search.Pull(context.Background(), selection, search.Connection{Endpoint: "https://" + address, TLS: clientTLS, Token: "test-token", Transport: protocol}, &rendered); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(rendered.Bytes(), expected.Bytes()) {
				t.Fatal("CLI pull differed from local output")
			}
			t.Logf("PIL schemas and events verified over HTTP/%d; decoded CLI output matches local search", major)
		})
	}
}

func TestPILFallsBackToHTTP2BeforeEventPull(t *testing.T) {
	selection, _ := events(t)
	h, err := New(selection.Root, "", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan string, 4)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Proto + " " + r.Method + " " + r.URL.Path
		h.ServeHTTP(w, r)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	var output bytes.Buffer
	if err := search.Pull(context.Background(), selection, search.Connection{Endpoint: server.URL, TLS: server.Client().Transport.(*http.Transport).TLSClientConfig}, &output); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/collect"} {
		if got := <-requests; got != "HTTP/2.0 GET "+path {
			t.Fatal(got)
		}
	}
	if len(requests) != 0 || strings.Count(output.String(), "\n") != 3 {
		t.Fatal("unexpected retries or event count")
	}
}

// The browser needs in-body success and tap identity; it cannot inspect HTTP trailers.
func TestCollectVersionNegotiationOverHTTP2AndHTTP3(t *testing.T) {
	selection, _ := events(t)
	serverTLS, clientTLS := credentials(t)
	address, _ := start(t, Config{Root: selection.Root, Address: "127.0.0.1:0", Socket: search.SocketPath(selection.Root), TLS: serverTLS, Token: "test-token"})
	for _, major := range []int{2, 3} {
		t.Run(fmt.Sprintf("HTTP%d", major), func(t *testing.T) {
			var tr http.RoundTripper
			if major == 3 {
				h := &http3.Transport{TLSClientConfig: clientTLS}
				tr = h
				defer h.Close()
			} else {
				h := &http.Transport{TLSClientConfig: clientTLS, ForceAttemptHTTP2: true}
				tr = h
				defer h.CloseIdleConnections()
			}
			client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
			pull := func(version, definition string, known ...string) ([]byte, *http.Response) {
				req, _ := http.NewRequest("GET", "https://"+address+"/collect?"+selection.Query().Encode(), nil)
				req.Header.Set("Authorization", "Bearer test-token")
				req.Header.Set("Accept", "Application/PIL")
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("PIL-Version", version)
				req.Header.Set("Definition-Version", definition)
				req.Header.Set("Definition-Known", strings.Join(known, ","))
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				return data, resp
			}
			cold, resp := pull("2", "")
			if resp.StatusCode != 200 || resp.ProtoMajor != major || resp.Header.Get("Content-Type") != "application/pil" || resp.Header.Get("PIL-Version") != "2" || resp.Header.Get("Trunk-Completion") != "body" || !bytes.HasPrefix(cold, []byte("TPIL\x02\x01")) || !bytes.HasSuffix(cold, []byte{255, 254}) {
				t.Fatalf("cold collect: %v %x", resp, cold)
			}
			version := resp.Header.Get("Definition-Version")
			if len(version) != 64 {
				t.Fatal("missing definition version")
			}
			cached, resp := pull("2", version)
			if resp.StatusCode != 200 || resp.Header.Get("Definition-Version") != version || !bytes.HasPrefix(cached, []byte("TPIL\x02\x02")) || !bytes.HasSuffix(cached, []byte{255, 254}) || len(cached) >= len(cold) {
				t.Fatalf("cached collect: %v %x", resp, cached)
			}
			stale, resp := pull("2", strings.Repeat("0", 64))
			if resp.StatusCode != 200 || !bytes.Equal(stale, cold) {
				t.Fatal("stale collect did not refresh definitions before events")
			}
			catalogs, err := store.ReadCatalogs(context.Background(), selection.Root, selection.Tap)
			if err != nil {
				t.Fatal(err)
			}
			var known []string
			for _, c := range catalogs {
				known = append(known, fmt.Sprintf("%x", c.Revision.Digest))
			}
			reused, resp := pull("2", "", known...)
			if resp.StatusCode != 200 || !bytes.HasPrefix(reused, []byte("TPIL\x02\x02")) || len(reused) >= len(cold) {
				t.Fatal("persisted definitions were resent")
			}
			_, resp = pull("999", version)
			if resp.StatusCode != 426 || resp.Header.Get("PIL-Version") != "2" {
				t.Fatal("unsupported protocol was not negotiated")
			}
		})
	}
}
