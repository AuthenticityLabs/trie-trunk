package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"strconv"
	"time"

	"trie-trunk/go/server/ingest"
)

func runIngest(args []string) error { return runIngestContext(context.Background(), args) }

func runIngestContext(ctx context.Context, args []string) error {
	portText := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		portText = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet("ingest", flag.ContinueOnError)
	flags.String("env", ".env", "environment file; empty disables file loading")
	root := flags.String("dir", "./caches", "cache root")
	cacheName := flags.String("cache", "", "registered cache")
	tenant := flags.String("tenant", "", "cache tenant owner")
	definition := flags.String("definition", "", "optional unified PIL/resource definition; otherwise accept client registration or blind CBOR")
	cert := flags.String("cert", "", "TLS certificate")
	key := flags.String("key", "", "TLS private key")
	ca := flags.String("client-ca", "", "client certificate CA (TLS requires this or TRUNK_TOKEN)")
	host := flags.String("host", "", "listen host; default loopback locally, wildcard with TLS")
	network := flags.String("network", "", "explicit tcp4/tcp6 or udp4/udp6")
	transport := flags.String("transport", "http", "http (HTTP/2 with TLS) or http3")
	interval := flags.Duration("flush", time.Second, "mature staging promotion interval")
	if e := flags.Parse(args); e != nil {
		return e
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	env, e := applyEnvironment(flags, "dir", "cert", "key", "client-ca", "host", "network", "transport", "flush", "cache", "tenant", "definition")
	if e != nil {
		return e
	}
	if portText == "" {
		var ok bool
		portText, ok = env.lookup("TRUNK_PORT")
		if !ok {
			portText = "5140"
		}
	}
	port, e := strconv.Atoi(portText)
	if e != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid port; positional port or TRUNK_PORT must be 1..65535")
	}
	if *interval <= 0 {
		return fmt.Errorf("flush interval must be positive")
	}
	if *transport != "http" && *transport != "http3" {
		return fmt.Errorf("unknown transport")
	}
	token, _ := env.lookup("TRUNK_TOKEN")
	local := *transport == "http" && token == "" && *cert == "" && *key == "" && *ca == ""
	var tlsConfig *tls.Config
	if local {
		if *host == "" || *host == "localhost" {
			*host = "127.0.0.1"
			if *network == "tcp6" {
				*host = "::1"
			}
		}
		if !net.ParseIP(*host).IsLoopback() {
			return fmt.Errorf("local ingestion requires a loopback --host; remote listeners require --cert, --key and TRUNK_TOKEN or --client-ca")
		}
	} else {
		if *cert == "" || *key == "" {
			return fmt.Errorf("TLS ingestion requires both --cert and --key; omit TLS/auth settings for local HTTP PIL ingestion")
		}
		if token == "" && *ca == "" {
			return fmt.Errorf("TLS ingestion requires TRUNK_TOKEN or --client-ca")
		}
		tlsConfig, e = serverTLS(*cert, *key, *ca)
		if e != nil {
			return e
		}
	}
	native, release, e := openNative(ctx, *root, *cacheName, *tenant, *definition)
	if e != nil {
		return e
	}
	handler := native.Handler(token)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	flushDone := make(chan error, 1)
	go func() {
		e := native.Run(ctx, *interval)
		if e != nil {
			cancel()
		}
		flushDone <- e
	}()
	addr := net.JoinHostPort(*host, strconv.Itoa(port))
	if *transport == "http3" {
		n := *network
		if n == "" {
			n = "udp"
		}
		if n != "udp" && n != "udp4" && n != "udp6" {
			e = fmt.Errorf("http3 requires UDP network")
		} else {
			var socket net.PacketConn
			socket, e = net.ListenPacket(n, addr)
			if e == nil {
				log.Printf("ingest http3 listening on https://%s", socket.LocalAddr())
				e = ingest.ServeHTTP3(ctx, socket, tlsConfig, handler)
				socket.Close()
			}
		}
	} else {
		n := *network
		if n == "" {
			n = "tcp"
		}
		if n != "tcp" && n != "tcp4" && n != "tcp6" {
			e = fmt.Errorf("http requires TCP network")
		} else {
			var listener net.Listener
			listener, e = net.Listen(n, addr)
			if e == nil {
				if local {
					log.Printf("ingest PIL listening on http://%s (loopback, no authentication)", listener.Addr())
					e = ingest.ServeLocal(ctx, listener, handler)
				} else {
					log.Printf("ingest PIL listening on https://%s", listener.Addr())
					e = ingest.ServeTLS(ctx, listener, tlsConfig, handler)
				}
				listener.Close()
			}
		}
	}
	cancel()
	return errors.Join(e, <-flushDone, release())
}
