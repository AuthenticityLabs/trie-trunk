package pil

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"trie-trunk/go/codec"
	"trie-trunk/go/server/cache"
)

func TestTimedBatchAcknowledgementAndCompression(t *testing.T) {
	endpoint, err := cache.OpenEndpoint(cache.Config{Root: t.TempDir(), Cache: "demo", Tenant: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	var requests, received atomic.Int32
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/ingest" {
			requests.Add(1)
			if r.Header.Get("Content-Encoding") != "gzip" {
				t.Error("batch did not use HTTP compression")
			}
		}
		rec := httptest.NewRecorder()
		endpoint.ServeHTTP(rec, r)
		if r.URL.Path == "/ingest" && rec.Code == 200 {
			received.Add(1)
		}
		return rec.Result(), nil
	})}
	logger, err := Open(context.Background(), Config{URL: "https://test.local", Cache: "demo", Definition: definition(t), HTTPClient: client, BatchMilliseconds: 60000})
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	layout, _ := cache.ParseDefinition(definition(t))
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		id, _ := codec.NewUUIDv7(time.Now())
		raw, err := layout.Encode(nil, []Value{{State: Direct, Bytes: id[:]}, cache.Uint(1, 1)})
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := logger.WriteFrames(context.Background(), raw); err != nil {
				t.Error(err)
			}
			if received.Load() == 0 {
				t.Error("write returned before acknowledgement")
			}
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		logger.batch.mu.Lock()
		n := len(logger.batch.queue)
		logger.batch.mu.Unlock()
		if n == 16 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("writers did not queue")
		}
		time.Sleep(time.Millisecond)
	}
	if requests.Load() != 0 {
		t.Fatal("quantity triggered batch")
	}
	if err := logger.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if requests.Load() != 1 {
		t.Fatal("not one batch", requests.Load())
	}
}

func TestTimedBatchTimerAndClose(t *testing.T) {
	for _, closeEarly := range []bool{false, true} {
		t.Run(map[bool]string{false: "timer", true: "close"}[closeEarly], func(t *testing.T) {
			d := definition(t)
			var sent atomic.Int32
			transport := transportFunc(func(r *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("PIL-Version", "2")
				rec.Header().Set("Definition-Version", r.Header.Get("Definition-Version"))
				if r.URL.Path == "/demo/registries" {
					rec.WriteHeader(204)
				} else {
					_, _ = io.Copy(io.Discard, r.Body)
					sent.Add(1)
					rec.Header().Set("Trunk-Acknowledgement", "durable-staging")
					rec.Header().Set("Trunk-Received", "1")
				}
				return rec.Result(), nil
			})
			logger, err := Open(context.Background(), Config{URL: "https://test.local", Cache: "demo", Definition: d, HTTPClient: &http.Client{Transport: transport}, BatchMilliseconds: 20})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				done <- logger.WriteValues(context.Background(), []Value{{State: Absent}, cache.Text("authorized")})
			}()
			if closeEarly {
				for {
					logger.batch.mu.Lock()
					n := len(logger.batch.queue)
					logger.batch.mu.Unlock()
					if n > 0 {
						break
					}
					select {
					case err := <-done:
						t.Fatalf("unexpected early completion: %v", err)
					default:
					}
					time.Sleep(time.Millisecond)
				}
				if err := logger.Close(); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("batch never flushed")
			}
			if sent.Load() != 1 {
				t.Fatal(sent.Load())
			}
			if err := logger.Close(); err != nil {
				t.Fatal(err)
			}
			// A closed batcher may not retain borrowed event bytes.
			if err := logger.WriteFrames(context.Background(), bytes.Repeat([]byte{0}, 3)); err == nil {
				t.Fatal("closed batch accepted write")
			}
		})
	}
}
