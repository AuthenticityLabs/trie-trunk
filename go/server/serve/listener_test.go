package serve

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
	"trie-trunk/go/codec"
	"trie-trunk/go/server/search"
	"trie-trunk/go/server/store"
)

func credentials(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	pair := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	return &tls.Config{Certificates: []tls.Certificate{pair}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS13}, &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}
}

func events(t *testing.T) (search.Config, *store.Manager) {
	t.Helper()
	root := shortDirectory(t)
	at := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	definition := codec.Definition{Version: 42, Timezone: "UTC", Aggregations: []string{"count", "duration.count", "duration.sum", "duration.min", "duration.max"}, Fields: codec.Schema{8: {Name: "id", Encoding: "uuidv7"}, 12: {Name: "duration", Unit: "us"}, 15: {Name: "message"}}}
	rev, data, err := definition.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	writer, err := store.Open(store.Config{Root: root, Tap: "test", Now: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Close() })
	if err := writer.Install(rev, data); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		id, err := codec.NewUUIDv7(at.Add(time.Duration(i) * time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		record, err := codec.MarshalCompact(codec.Record{8: id, 12: uint64(2234), 15: fmt.Sprintf("event %d", i)}, codec.CBOR)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = writer.Append(rev, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	return search.Config{Root: root, Tap: "test", Start: at, End: at.Add(time.Hour)}, writer
}

func start(t *testing.T, c Config) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	c.Ready = func(addr string) { ready <- addr }
	go func() { done <- Run(ctx, c) }()
	var address string
	select {
	case address = <-ready:
	case err := <-done:
		cancel()
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("listener startup timeout")
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("listener shutdown timeout")
		}
	}
	t.Cleanup(stop)
	return address, stop
}

func TestHTTP2HTTP3AndSocket(t *testing.T) {
	selection, _ := events(t)
	serverTLS, clientTLS := credentials(t)
	socket := search.SocketPath(selection.Root)
	web := t.TempDir()
	if err := os.Mkdir(filepath.Join(web, "assets"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"index.html": "<!doctype html><title>trie-trunk</title>", "assets/app.js": "document.title='trie-trunk';"} {
		if err := os.WriteFile(filepath.Join(web, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	address, stop := start(t, Config{Root: selection.Root, Web: web, Address: "127.0.0.1:0", Socket: socket, TLS: serverTLS, Token: "test-token"})
	var expected bytes.Buffer
	if err := search.Run(context.Background(), selection, &expected); err != nil {
		t.Fatal(err)
	}
	for _, pretty := range []bool{false, true} {
		selection.Pretty = pretty
		expected.Reset()
		if err := search.Run(context.Background(), selection, &expected); err != nil {
			t.Fatal(err)
		}
		for _, connection := range []search.Connection{{Socket: socket}, {Endpoint: "https://" + address, TLS: clientTLS, Token: "test-token"}} {
			var output bytes.Buffer
			if err := search.Pull(context.Background(), selection, connection, &output); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(output.Bytes(), expected.Bytes()) {
				t.Fatalf("stream differs: %s", output.String())
			}
		}
	}
	selection.Pretty = false
	compact := selection
	compact.Compact = true
	var dictionary bytes.Buffer
	if err := search.Run(context.Background(), compact, &dictionary); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(selection)
	for _, version := range []int{2, 3} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			var transport http.RoundTripper
			if version == 2 {
				tr := &http.Transport{TLSClientConfig: clientTLS, ForceAttemptHTTP2: true}
				transport = tr
				defer tr.CloseIdleConnections()
			} else {
				tr := &http3.Transport{TLSClientConfig: clientTLS}
				transport = tr
				defer tr.Close()
			}
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			tapsRequest, _ := http.NewRequest(http.MethodGet, "https://"+address+"/caches", nil)
			tapsRequest.Header.Set("Authorization", "Bearer test-token")
			tapsResponse, err := client.Do(tapsRequest)
			if err != nil {
				t.Fatal(err)
			}
			tapsBody, err := io.ReadAll(tapsResponse.Body)
			tapsResponse.Body.Close()
			if err != nil || tapsResponse.StatusCode != 200 || tapsResponse.ProtoMajor != version || string(tapsBody) != "{\"caches\":[\"test\"]}\n" {
				t.Fatal(tapsResponse.Status, string(tapsBody), err)
			}
			for _, test := range []struct {
				method string
				path   string
				status int
				body   string
				typeOf string
			}{
				{http.MethodGet, "/", 200, "<!doctype html><title>trie-trunk</title>", "text/html"},
				{http.MethodGet, "/assets/app.js", 200, "document.title='trie-trunk';", "javascript"},
				{http.MethodHead, "/", 200, "", "text/html"},
				{http.MethodGet, "/assets/missing.js", 404, "404 page not found\n", "text/plain"},
				{http.MethodPost, "/", 405, "method not allowed\n", "text/plain"},
			} {
				req, _ := http.NewRequest(test.method, "https://"+address+test.path, nil)
				response, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || response.StatusCode != test.status || response.ProtoMajor != version || string(body) != test.body || !strings.Contains(response.Header.Get("Content-Type"), test.typeOf) {
					t.Fatalf("%s %s: proto=%s status=%d body=%s content-type=%s error=%v", test.method, test.path, response.Proto, response.StatusCode, body, response.Header.Get("Content-Type"), err)
				}
			}
			req, _ := http.NewRequest(http.MethodPost, "https://"+address+"/events", bytes.NewReader(data))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer test-token")
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || response.StatusCode != 200 || response.ProtoMajor != version || response.Trailer.Get("Trunk-Status") != "ok" {
				t.Fatalf("proto=%s status=%d body=%s error=%v trailer=%v", response.Proto, response.StatusCode, body, err, response.Trailer)
			}
			if version == 2 && !strings.Contains(response.Header.Get("Alt-Svc"), "h3=") {
				t.Fatal("no HTTP/3 advertisement")
			}
			negotiateRequest, _ := http.NewRequest(http.MethodPost, "https://"+address+"/schemas", strings.NewReader(`{"tap":"test"}`))
			negotiateRequest.Header.Set("Content-Type", "application/json")
			negotiateRequest.Header.Set("Authorization", "Bearer test-token")
			negotiateRequest.Header.Set("Accept", "application/json")
			negotiateResponse, err := client.Do(negotiateRequest)
			if err != nil {
				t.Fatal(err)
			}
			var negotiation schemaResponse
			err = json.NewDecoder(negotiateResponse.Body).Decode(&negotiation)
			negotiateResponse.Body.Close()
			if err != nil || negotiateResponse.StatusCode != 200 || negotiateResponse.ProtoMajor != version || len(negotiation.Schemas) != 1 || negotiation.Schemas[0].Dictionary == nil {
				t.Fatal(negotiation, err)
			}
			compact.Negotiation = negotiation.Negotiation
			compactData, err := json.Marshal(compact)
			if err != nil {
				t.Fatal(err)
			}
			compactRequest, _ := http.NewRequest(http.MethodPost, "https://"+address+"/events", bytes.NewReader(compactData))
			compactRequest.Header.Set("Content-Type", "application/json")
			compactRequest.Header.Set("Authorization", "Bearer test-token")
			compactRequest.Header.Set("Accept", search.DictionaryJSONL)
			compactResponse, err := client.Do(compactRequest)
			if err != nil {
				t.Fatal(err)
			}
			compactBody, err := io.ReadAll(compactResponse.Body)
			compactResponse.Body.Close()
			if err != nil || compactResponse.ProtoMajor != version || compactResponse.StatusCode != 200 || compactResponse.Header.Get("Content-Type") != search.DictionaryJSONL || compactResponse.Trailer.Get("Trunk-Status") != "ok" || !bytes.Equal(compactBody, dictionary.Bytes()) {
				t.Fatalf("dictionary stream mismatch: %s %s %v", compactResponse.Status, compactBody, err)
			}
			req, _ = http.NewRequest(http.MethodPost, "https://"+address+"/events", bytes.NewReader(data))
			req.Header.Set("Content-Type", "application/json")
			response, err = client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != 401 {
				t.Fatal(response.Status)
			}
		})
	}
	unauthorized := clientTLS.Clone()
	unauthorized.Certificates = nil
	err := search.Pull(context.Background(), selection, search.Connection{Endpoint: "https://" + address, TLS: unauthorized, Token: "test-token"}, io.Discard)
	if err == nil {
		t.Fatal("missing client certificate accepted")
	}
	stat, err := os.Stat(socket)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal(stat, err)
	}
	stop()
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatal("socket was not removed", err)
	}
}

func TestFollowDisconnectAndShutdown(t *testing.T) {
	selection, _ := events(t)
	tlsConfig, _ := credentials(t)
	socket := search.SocketPath(selection.Root)
	_, stop := start(t, Config{Root: selection.Root, Address: "127.0.0.1:0", Socket: socket, TLS: tlsConfig, MaxQueries: 1})
	selection.Follow = true
	selection.End = time.UnixMilli(1 << 48)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	read, write := io.Pipe()
	defer read.Close()
	done := make(chan error, 1)
	go func() {
		err := search.Pull(ctx, selection, search.Connection{Socket: socket}, write)
		write.CloseWithError(err)
		done <- err
	}()
	decoder := json.NewDecoder(read)
	for i := 0; i < 3; i++ {
		var record map[string]any
		if err := decoder.Decode(&record); err != nil {
			t.Fatal(err)
		}
	}
	selection.Follow = false
	selection.End = selection.Start.Add(time.Hour)
	if err := search.Pull(context.Background(), selection, search.Connection{Socket: socket}, io.Discard); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatal("follower did not hold query slot", err)
	}
	cancel()
	read.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation lost")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follow did not stop")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := search.Pull(context.Background(), selection, search.Connection{Socket: socket}, io.Discard)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slot leaked", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	selection.Follow = true
	selection.End = time.UnixMilli(1 << 48)
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- search.Pull(ctx, selection, search.Connection{Socket: socket}, io.Discard) }()
	time.Sleep(100 * time.Millisecond)
	stop()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("shutdown reported completed stream")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown left follower running")
	}
}

func TestSocketDoesNotReplaceFilesOrListeners(t *testing.T) {
	dir := filepath.Join(shortDirectory(t), "private")
	name := filepath.Join(dir, "search.sock")
	listener, err := listenUnix(name)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if second, err := listenUnix(name); err == nil {
		second.Close()
		t.Fatal("replaced active listener")
	}
	listener.Close()
	if err := os.WriteFile(name, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenUnix(name); err == nil {
		t.Fatal("replaced regular file")
	}
	data, _ := os.ReadFile(name)
	if string(data) != "keep" {
		t.Fatal("changed regular file")
	}
}

func shortDirectory(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "trunk-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestLoopbackHTTPOrigin(t *testing.T) {
	for _, token := range []string{"", "test-token"} {
		t.Run("token="+token, func(t *testing.T) {
			selection, _ := events(t)
			serverTLS, clientTLS := credentials(t)
			web := t.TempDir()
			if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<title>snoot</title>"), 0644); err != nil {
				t.Fatal(err)
			}
			ready := make(chan string, 1)
			address, stop := start(t, Config{
				Root: selection.Root, Web: web, Address: "127.0.0.1:0", Socket: search.SocketPath(selection.Root),
				TLS: serverTLS, Token: token, HTTPAddress: "127.0.0.1:0", HTTPReady: func(a string) { ready <- a },
			})
			origin := <-ready
			tr := &http.Transport{}
			defer tr.CloseIdleConnections()
			client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
			request := func(path, auth, definition string) ([]byte, *http.Response) {
				t.Helper()
				req, _ := http.NewRequest("GET", "http://"+origin+path, nil)
				if strings.HasPrefix(path, "/collect") {
					req.Header.Set("Accept", search.PIL)
					req.Header.Set("PIL-Version", "2")
					req.Header.Set("Definition-Version", definition)
				}
				if auth != "" {
					req.Header.Set("Authorization", "Bearer "+auth)
				}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if resp.Header.Get("Alt-Svc") != "" {
					t.Fatal("HTTP origin leaked direct QUIC address")
				}
				return body, resp
			}
			body, resp := request("/", "", "")
			if resp.StatusCode != 200 || string(body) != "<title>snoot</title>" {
				t.Fatalf("static: %s %s", resp.Status, body)
			}
			if token != "" {
				for _, auth := range []string{"", "wrong"} {
					_, resp = request("/caches", auth, "")
					if resp.StatusCode != 401 {
						t.Fatalf("missing auth check: %s", resp.Status)
					}
				}
			}
			body, resp = request("/caches", token, "")
			if resp.StatusCode != 200 || !bytes.Contains(body, []byte(`"test"`)) {
				t.Fatalf("taps: %s %s", resp.Status, body)
			}
			body, resp = request("/collect?"+selection.Query().Encode(), token, "")
			if resp.StatusCode != 200 || !bytes.HasPrefix(body, []byte("TPIL\x02\x01")) || resp.Header.Get("Definition-Version") == "" {
				t.Fatalf("collect: %s %x", resp.Status, body)
			}
			var got, expected bytes.Buffer
			if err := search.DecodePIL(context.Background(), selection, io.NopCloser(bytes.NewReader(body)), &got); err != nil {
				t.Fatal(err)
			}
			if err := search.Run(context.Background(), selection, &expected); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Bytes(), expected.Bytes()) {
				t.Fatal("HTTP PIL differs from direct search")
			}
			body, resp = request("/collect?"+selection.Query().Encode(), token, resp.Header.Get("Definition-Version"))
			if resp.StatusCode != 200 || !bytes.HasPrefix(body, []byte("TPIL\x02\x02")) {
				t.Fatal("cached definition was not reused")
			}
			// The optional HTTP origin must not change TLS client authentication.
			noIdentity := clientTLS.Clone()
			noIdentity.Certificates = nil
			tlsTransport := &http.Transport{TLSClientConfig: noIdentity}
			defer tlsTransport.CloseIdleConnections()
			if r, err := (&http.Client{Transport: tlsTransport, Timeout: 5 * time.Second}).Get("https://" + address + "/"); err == nil {
				r.Body.Close()
				t.Fatal("TLS listener accepted a missing client certificate")
			}
			stop()
			if c, err := net.DialTimeout("tcp", origin, time.Second); err == nil {
				c.Close()
				t.Fatal("HTTP origin still listening after shutdown")
			}
		})
	}
}

func TestHTTPOriginRejectsNonLoopback(t *testing.T) {
	serverTLS, _ := credentials(t)
	for _, address := range []string{":3020", "0.0.0.0:3020", "[::]:3020", "192.168.1.2:3020", "localhost:3020", "127.0.0.1"} {
		err := Run(context.Background(), Config{TLS: serverTLS, Socket: "unused", HTTPAddress: address})
		if err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Fatalf("%q: %v", address, err)
		}
	}
}
