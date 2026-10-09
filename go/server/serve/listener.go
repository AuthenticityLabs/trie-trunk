package serve

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

type Config struct {
	Root    string
	Web     string
	Address string
	// HTTPAddress optionally exposes the same routes to a trusted local reverse
	// proxy. It must use a literal loopback IP. TLS client authentication does
	// not apply here; the proxy owns that boundary. Bearer checks still apply.
	HTTPAddress string
	HTTPReady   func(string)
	Socket      string
	TLS         *tls.Config
	Token       string
	Headers     []string
	MaxQueries  int
	Ready       func(string)
	OnError     func(error)
}

func Run(ctx context.Context, c Config) error {
	if c.TLS == nil || len(c.TLS.Certificates) == 0 || c.Socket == "" {
		return fmt.Errorf("TLS certificate and Unix socket required")
	}
	if c.Token == "" && c.TLS.ClientAuth != tls.RequireAndVerifyClientCert {
		return fmt.Errorf("token or verified client certificates required")
	}
	if c.HTTPAddress != "" {
		host, _, err := net.SplitHostPort(c.HTTPAddress)
		if err != nil || !net.ParseIP(host).IsLoopback() {
			return fmt.Errorf("HTTP origin address must use a loopback IP and port")
		}
	}
	if c.MaxQueries == 0 {
		c.MaxQueries = 4
	}
	handler, err := New(c.Root, c.Token, c.Headers, c.MaxQueries)
	if err != nil {
		return err
	}
	defer handler.Close()
	handler.OnError = c.OnError
	routes := http.NewServeMux()
	routes.Handle("/events", handler)
	routes.Handle("/collect", handler)
	routes.Handle("/schemas", handler)
	routes.Handle("/taps", handler)
	if c.Web != "" {
		routes.Handle("/", staticFiles(c.Web))
	}
	tcp, err := net.Listen("tcp", c.Address)
	if err != nil {
		return err
	}
	defer tcp.Close()
	udp, err := net.ListenPacket("udp", tcp.Addr().String())
	if err != nil {
		return err
	}
	defer udp.Close()
	var origin net.Listener
	if c.HTTPAddress != "" {
		origin, err = net.Listen("tcp", c.HTTPAddress)
		if err != nil {
			return err
		}
		defer origin.Close()
	}
	local, err := listenUnix(c.Socket)
	if err != nil {
		return err
	}
	defer local.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	tlsConfig := c.TLS.Clone()
	tlsConfig.MinVersion = tls.VersionTLS13
	h3 := &http3.Server{Handler: routes, TLSConfig: tlsConfig.Clone(), MaxHeaderBytes: 16384, QUICConfig: &quic.Config{Allow0RTT: false, MaxIncomingStreams: 64, MaxIdleTimeout: 5 * time.Minute, KeepAlivePeriod: 15 * time.Second}}
	_, port, _ := net.SplitHostPort(tcp.Addr().String())
	h3.Port, _ = strconv.Atoi(port)
	remote := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = h3.SetQUICHeaders(w.Header())
		routes.ServeHTTP(w, r)
	})
	base := func(net.Listener) context.Context { return ctx }
	h2 := &http.Server{Handler: remote, TLSConfig: tlsConfig, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16384, BaseContext: base}
	localHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.serve(w, r, false) })
	unix := &http.Server{Handler: localHandler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16384, BaseContext: base}
	done := make(chan error, 4)
	remaining := 3
	var originServer *http.Server
	if origin != nil {
		// No direct-origin Alt-Svc: the public proxy owns HTTP/2 and HTTP/3.
		originServer = &http.Server{Handler: routes, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16384, BaseContext: base}
		remaining++
		go func() { done <- originServer.Serve(origin) }()
	}
	go func() { done <- h2.ServeTLS(tcp, "", "") }()
	go func() { done <- h3.Serve(udp) }()
	go func() { done <- unix.Serve(local) }()
	if c.Ready != nil {
		c.Ready(tcp.Addr().String())
	}
	if origin != nil && c.HTTPReady != nil {
		c.HTTPReady(origin.Addr().String())
	}
	var result error
	select {
	case <-ctx.Done():
	case result = <-done:
		remaining--
	}
	cancel()
	_ = h2.Close()
	_ = h3.Close()
	_ = unix.Close()
	if originServer != nil {
		_ = originServer.Close()
	}
	for ; remaining > 0; remaining-- {
		<-done
	}
	if errors.Is(result, http.ErrServerClosed) {
		return nil
	}
	return result
}

func listenUnix(name string) (*net.UnixListener, error) {
	dir := filepath.Dir(name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("socket directory must be a private directory (0700): %s", dir)
	}
	if _, err := os.Lstat(name); err == nil {
		return nil, fmt.Errorf("socket path already exists: %s; stop its owner or remove a stale socket", name)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: name, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(name, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}
