package search

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPullEncodesOpenAndExplicitFollowWindows(t *testing.T) {
	from := time.Date(2026, 9, 25, 19, 23, 56, 123456789, time.UTC)
	explicitEnd := from.Add(time.Hour)
	for _, test := range []struct {
		name    string
		end     time.Time
		follow  bool
		omitEnd bool
	}{
		{"open follow", time.UnixMilli(1 << 48), true, true},
		{"bounded follow", explicitEnd, true, false},
		{"finite search", explicitEnd, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			received := make(chan url.Values, 1)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/collect" || r.ContentLength != 0 || r.Header.Get("PIL-Version") != "2" {
					t.Error("expected a single GET collection")
				}

				if r.Header.Get("Accept") != PIL {
					t.Error("PIL was not requested")
				}
				query := r.URL.Query()
				received <- query
				w.Header().Set("Content-Type", PIL)
				w.Header().Set("PIL-Version", "2")
				w.Header().Set("Definition-Version", strings.Repeat("a", 64))
				w.Header().Set("Trunk-Completion", "body")
				w.Header().Set("Trailer", "Trunk-Status")
				w.WriteHeader(200)
				w.Write([]byte("TPIL\x02\xff\xfe"))
				w.Header().Set("Trunk-Status", "ok")
			}))
			server.EnableHTTP2 = true
			server.StartTLS()
			defer server.Close()
			c := Config{Root: "./logs", Start: from, End: test.end, Follow: test.follow}
			connection := Connection{Endpoint: server.URL, Transport: "http2", TLS: server.Client().Transport.(*http.Transport).TLSClientConfig}
			var output bytes.Buffer
			if err := Pull(context.Background(), c, connection, &output); err != nil {
				t.Fatal(err)
			}
			query := <-received
			end, ok := query["to"]
			if ok == test.omitEnd {
				t.Fatalf("unexpected end field: %s", end)
			}
			if ok {
				decoded, err := time.Parse(time.RFC3339Nano, end[0])
				if err != nil || !decoded.Equal(test.end) {
					t.Fatal(end, err)
				}
			}
			decodedStart, err := time.Parse(time.RFC3339Nano, query.Get("from"))
			if err != nil || !decodedStart.Equal(from) {
				t.Fatal("start changed", err)
			}
			follow, err := strconv.ParseBool(query.Get("follow"))
			if err != nil || follow != test.follow {
				t.Fatal("follow changed", err)
			}
			if !c.End.Equal(test.end) {
				t.Fatal("caller end changed")
			}
			if output.String() != "" {
				t.Fatal(output.String())
			}
		})
	}
}

func TestPullNormalizesCancellationCause(t *testing.T) {
	ready := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(ready)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	now := time.Now().UTC()
	config := Config{Root: "./logs", Start: now.Add(-time.Hour), End: time.UnixMilli(1 << 48), Follow: true}
	connection := Connection{Endpoint: server.URL, TLS: server.Client().Transport.(*http.Transport).TLSClientConfig}
	done := make(chan error, 1)
	go func() { done <- Pull(ctx, config, connection, io.Discard) }()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach server")
	}
	cancel(errors.New("interrupt signal received"))
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation was not preserved: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled request did not stop")
	}
}

func TestPullRejectsIncompletePILResponses(t *testing.T) {
	for _, test := range []struct{ name, body, status string }{
		{"empty", "", "ok"},
		{"missing success", "TPIL\x02\xff", "ok"},
		{"trailing data", "TPIL\x02\xff\xfeX", "ok"},
		{"premature success", "TPIL\x02\xfe", "ok"},
		{"missing end", "TPIL\x02", "ok"},
		{"error trailer", "TPIL\x02\xff\xfe", "error"},
		{"missing trailer", "TPIL\x02\xff\xfe", ""},
		{"unknown record", "TPIL\x02\x80\xff", "ok"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Trailer", "Trunk-Status")

				w.Header().Set("Content-Type", PIL)
				w.Header().Set("PIL-Version", "2")
				w.Header().Set("Definition-Version", strings.Repeat("a", 64))
				w.Header().Set("Trunk-Completion", "body")
				w.WriteHeader(200)
				w.Write([]byte(test.body))
				w.Header().Set("Trunk-Status", test.status)
			}))
			server.EnableHTTP2 = true
			server.StartTLS()
			defer server.Close()
			now := time.Now()
			config := Config{Root: "/unused", Start: now.Add(-time.Hour), End: now}
			conn := Connection{Endpoint: server.URL, Transport: "http2", TLS: server.Client().Transport.(*http.Transport).TLSClientConfig}
			if err := Pull(context.Background(), config, conn, io.Discard); err == nil {
				t.Fatal("incomplete stream reported success")
			}
		})
	}
}
