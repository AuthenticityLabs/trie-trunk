package ingest

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

func ServeHTTP3(ctx context.Context, conn net.PacketConn, tlsConfig *tls.Config, h http.Handler) error {
	if conn == nil || tlsConfig == nil || h == nil {
		return fmt.Errorf("socket, TLS config and handler required")
	}
	config := tlsConfig.Clone()
	config.MinVersion = tls.VersionTLS13
	server := &http3.Server{Handler: h, TLSConfig: config, MaxHeaderBytes: 8192, QUICConfig: &quic.Config{Allow0RTT: false, MaxIncomingStreams: 64, MaxIdleTimeout: time.Minute}}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = server.Close()
		case <-done:
		}
	}()
	e := server.Serve(conn)
	if ctx.Err() != nil {
		return nil
	}
	return e
}

func ServeTLS(ctx context.Context, listener net.Listener, tlsConfig *tls.Config, h http.Handler) error {
	if listener == nil || tlsConfig == nil || h == nil {
		return fmt.Errorf("listener, TLS config and handler required")
	}
	config := tlsConfig.Clone()
	config.MinVersion = tls.VersionTLS13
	return serveHTTP(ctx, tls.NewListener(listener, config), h)
}

func ServeLocal(ctx context.Context, listener net.Listener, h http.Handler) error {
	if listener == nil || h == nil {
		return fmt.Errorf("listener and handler required")
	}
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !addr.IP.IsLoopback() {
		return fmt.Errorf("local ingestion requires a loopback TCP listener")
	}
	return serveHTTP(ctx, listener, h)
}

func serveHTTP(ctx context.Context, listener net.Listener, h http.Handler) error {
	server := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 8192, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = server.Close()
		case <-done:
		}
	}()
	e := server.Serve(listener)
	if errors.Is(e, http.ErrServerClosed) || ctx.Err() != nil {
		return nil
	}
	return e
}
