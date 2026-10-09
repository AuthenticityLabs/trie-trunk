package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
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

	"trie-trunk/go/server/search"
)

func TestServiceCommandBoundaries(t *testing.T) {
	for _, args := range [][]string{{"ingest", "5410", "serve", "443"}, {"serve", "443", "ingest", "5410"}, {"ingest", "--dir", "serve", "serve", "443"}} {
		commands, err := splitServices(args)
		if err != nil || len(commands) != 2 {
			t.Fatal(commands, err)
		}
	}
	for _, args := range [][]string{{"ingest", "5410", "ingest"}, {"serve", "443", "serve"}, {"other"}} {
		if _, err := splitServices(args); err == nil {
			t.Fatal("accepted", args)
		}
	}
}

func TestCombinedIngestServeAndSearch(t *testing.T) {
	clearTrunkEnvironment(t)
	root, err := os.MkdirTemp("", "trunk-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	encodedKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(root, "cert.pem"), filepath.Join(root, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey}), 0600); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(root, "config.env")
	web := filepath.Join(root, "public")
	if err := os.Mkdir(web, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<title>trie-trunk</title>"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env, []byte(fmt.Sprintf("TRUNK_DIR=%s\nTRUNK_HOST=127.0.0.1\nTRUNK_TOKEN=testing\nTRUNK_CERT=%s\nTRUNK_KEY=%s\nTRUNK_WEB=%s\n", filepath.Join(root, "logs"), certFile, keyFile, web)), 0600); err != nil {
		t.Fatal(err)
	}
	ingestPort, servePort := freePort(t), freePort(t)
	for servePort == ingestPort {
		servePort = freePort(t)
	}
	httpPort := freePort(t)
	for httpPort == ingestPort || httpPort == servePort {
		httpPort = freePort(t)
	}
	f, err := os.OpenFile(env, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(f, "TRUNK_HTTP_ADDRESS=127.0.0.1:%s\n", httpPort)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"ingest", ingestPort, "--env", env, "serve", servePort, "--env", env}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServices(ctx, args) }()
	stopped := false
	defer func() {
		if !stopped {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("shutdown timeout")
			}
		}
	}()
	socket := search.SocketPath(filepath.Join(root, "logs"))
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatal(err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("startup timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	pool := x509.NewCertPool()
	leaf, _ := x509.ParseCertificate(der)
	pool.AddCert(leaf)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	req, _ := http.NewRequest("POST", "https://127.0.0.1:"+ingestPort+"/test", strings.NewReader("{\"service\":\"vault\",\"message\":\"combined services\"}\n"))
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("Authorization", "Bearer testing")
	var response *http.Response
	for {
		response, err = client.Do(req)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 204 {
		t.Fatalf("%s %s %v", response.Status, body, err)
	}
	var local, remote, direct bytes.Buffer
	common := []string{"--env", env, "--all", "--tap", "test"}
	if err := runSearch(ctx, common, &local); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(local.String(), "combined services") {
		t.Fatal(local.String())
	}
	if err := runSearch(ctx, append(append([]string{}, common...), "--endpoint", "https://127.0.0.1:"+servePort, "--ca", certFile), &remote); err != nil {
		t.Fatal(err)
	}
	if err := runSearch(ctx, append(append([]string{}, common...), "--local"), &direct); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(local.Bytes(), remote.Bytes()) || !bytes.Equal(local.Bytes(), direct.Bytes()) {
		t.Fatal("remote, socket and direct output differ")
	}
	response, err = client.Get("https://127.0.0.1:" + servePort + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "<title>trie-trunk</title>" {
		t.Fatalf("web status=%d body=%s error=%v", response.StatusCode, body, err)
	}
	response, err = client.Get("http://127.0.0.1:" + httpPort + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "<title>trie-trunk</title>" {
		t.Fatalf("HTTP origin status=%d body=%s error=%v", response.StatusCode, body, err)
	}
	cancel()
	select {
	case err := <-done:
		stopped = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timeout")
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatal("socket not cleaned", err)
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	listener.Close()
	return port
}
