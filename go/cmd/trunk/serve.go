package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"time"
	"trie-trunk/go/server/cache"
	"trie-trunk/go/server/search"
	"trie-trunk/go/server/serve"
)

func runServe(ctx context.Context, args []string) (result error) {
	portText := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		portText, args = args[0], args[1:]
	}
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.String("env", ".env", "environment file; empty disables file loading")
	cacheName := flags.String("cache", "", "serve a collection-native cache")
	tenant := flags.String("tenant", "", "cache tenant owner")
	definition := flags.String("definition", "", "unified PIL/resource definition for cache serving")
	root := flags.String("dir", "./taps", "store root")
	web := flags.String("web", "./web", "built web app directory; empty disables static files")
	host := flags.String("host", "127.0.0.1", "listen host")
	httpAddress := flags.String("http-address", "", "optional trusted reverse-proxy HTTP origin (loopback IP:port)")
	cert := flags.String("cert", "", "TLS server certificate")
	key := flags.String("key", "", "TLS server private key")
	ca := flags.String("client-ca", "", "required client certificate CA")
	socket := flags.String("socket", "", "Unix socket; default <dir>/.trunk/search.sock")
	queries := flags.Int("queries", 4, "maximum concurrent searches, including followers")
	headers := flags.String("entry-headers", "", "default JSON field order")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected serve arguments")
	}
	env, err := applyEnvironment(flags, "dir", "web", "host", "http-address", "cert", "key", "client-ca", "socket", "queries", "entry-headers", "cache", "tenant", "definition")
	if err != nil {
		return err
	}
	if *cacheName == "" && (*definition != "" || *tenant != "") {
		return fmt.Errorf("--definition and --tenant require --cache")
	}
	if portText == "" {
		portText, _ = env.lookup("TRUNK_SERVE_PORT")
		if portText == "" {
			portText = "443"
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid serve port; expected 1..65535")
	}
	if *cert == "" || *key == "" {
		return fmt.Errorf("HTTP/2 and HTTP/3 serving requires both --cert and --key")
	}
	token, _ := env.lookup("TRUNK_TOKEN")
	if token == "" && *ca == "" {
		return fmt.Errorf("serving requires TRUNK_TOKEN or --client-ca")
	}
	tlsConfig, err := serverTLS(*cert, *key, *ca)
	if err != nil {
		return err
	}
	if *socket == "" {
		*socket = search.SocketPath(*root)
	}
	var native *cache.Endpoint
	var api http.Handler
	if *cacheName != "" {
		var release func() error
		native, release, err = openNative(ctx, *root, *cacheName, *tenant, *definition)
		if err != nil {
			return err
		}
		defer func() { result = errors.Join(result, release()) }()
		api = native.Handler(token)
	}
	config := serve.Config{
		Root: *root, Web: *web, Address: net.JoinHostPort(*host, strconv.Itoa(port)), Socket: *socket,
		HTTPAddress: *httpAddress,
		TLS:         tlsConfig, Token: token, Headers: strings.Split(*headers, ","), MaxQueries: *queries,
		Ready: func(address string) {
			log.Printf("serve http2/http3 listening on https://%s; socket %s", address, *socket)
		},
		HTTPReady: func(address string) {
			log.Printf("serve HTTP origin listening on http://%s (trusted local proxy)", address)
		},
		OnError: func(err error) { log.Printf("search: %v", err) },
	}
	if api != nil {
		config.API = api
	}
	if native != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			e := native.Run(ctx, time.Second)
			if e != nil {
				cancel()
			}
			done <- e
		}()
		defer func() { cancel(); result = errors.Join(result, <-done) }()
	}
	err = serve.Run(ctx, config)
	if errors.Is(err, os.ErrPermission) && port < 1024 {
		return fmt.Errorf("serve port %d requires privileged-port binding permission; use 8443 or run with permission to bind port %d: %w", port, port, err)
	}
	return err

}
