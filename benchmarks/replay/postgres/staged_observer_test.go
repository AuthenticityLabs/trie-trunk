//go:build stagedobserver

package main

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
	"trie-trunk/go/server/cache"
)

func TestPublicationPrecedesDurableAckAndFailedPersistenceIsNotSuccess(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "durable", true: "failure-and-retry"}[fail], func(t *testing.T) {
			l := layout()
			ep, e := cache.OpenEndpoint(cache.Config{Root: t.TempDir(), Cache: "benchmark", Tenant: "benchmark", Layout: l, ObjectIndex: true, Now: func() time.Time { return start.Add(10 * time.Minute) }})
			if e != nil {
				t.Fatal(e)
			}
			defer ep.Close()
			server := httptest.NewServer(ep)
			defer server.Close()
			initial := fixture(1, 0, 256)
			if e = postPIL(server.Client(), server.URL, l, [][]byte{encode(l, initial, 1, false)}); e != nil {
				t.Fatal(e)
			}
			expected := []Object{initial, fixture(1, 1, 256)}
			clients, e := openStreamClients(l, initial, expected, 1, false)
			if e != nil {
				t.Fatal(e)
			}
			defer clients.close()
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			deferredFailure := errors.New("injected persistence-boundary failure")
			if e = setStagedObserver(func(frames [][]byte) error {
				if len(frames) != 1 {
					return errors.New("unexpected batch")
				}
				if e := clients.publish(frames[0]); e != nil {
					return e
				}
				<-release
				if fail {
					return deferredFailure
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			defer setStagedObserver(nil)
			raw := encode(l, expected[1], 1, false)
			done := make(chan error, 1)
			go func() { done <- postPIL(server.Client(), server.URL, l, [][]byte{raw}) }()
			select {
			case ack := <-clients.acks:
				if ack.err != nil || ack.revision != 1 {
					t.Fatal(ack)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no early publication")
			}
			select {
			case e := <-done:
				t.Fatalf("durable ack arrived while sync boundary blocked: %v", e)
			default:
			}
			unblock()
			e = <-done
			if (e != nil) != fail {
				t.Fatalf("unexpected durable outcome: %v", e)
			}
			if e = setStagedObserver(nil); e != nil {
				t.Fatal(e)
			}
			// The sender still owns the original bytes. Retry must deduplicate and durably
			// acknowledge, even when the subscriber already saw the first publication.
			if fail {
				if e = postPIL(server.Client(), server.URL, l, [][]byte{raw}); e != nil {
					t.Fatal(e)
				}
			}
			seen := 0
			e = ep.History(ctx, l.Definition.Identity, 1, cache.Uint(1, 8), stamp(1, 1, 1), stamp(1, 2, 1), func(_ cache.EventReference, b []byte) error {
				seen++
				if !bytes.Equal(b, raw) {
					return errors.New("history differs from publication")
				}
				return nil
			})
			if e != nil || seen != 1 {
				t.Fatalf("history count=%d error=%v", seen, e)
			}
		})
	}
}
