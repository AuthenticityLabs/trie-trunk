package search

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

type Connection struct {
	Endpoint  string
	Socket    string
	Token     string
	TLS       *tls.Config
	Transport string
}

func SocketPath(root string) string { return filepath.Join(root, ".trunk", "search.sock") }

func Pull(ctx context.Context, c Config, connection Connection, output io.Writer) (err error) {
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	if err := c.Validate(); err != nil {
		return err
	}
	if connection.Transport != "" && connection.Transport != "auto" && connection.Transport != "http2" && connection.Transport != "http3" {
		return fmt.Errorf("transport must be auto, http2 or http3")
	}
	if connection.Endpoint == "" && connection.Transport != "" && connection.Transport != "auto" {
		return fmt.Errorf("remote transport requires --endpoint; local search uses the Unix socket")
	}
	transport := &http.Transport{TLSClientConfig: connection.TLS, ForceAttemptHTTP2: true, TLSHandshakeTimeout: 10 * time.Second}
	defer transport.CloseIdleConnections()
	endpoint := connection.Endpoint
	if endpoint == "" {
		socket := connection.Socket
		if socket == "" {
			socket = SocketPath(c.Root)
		}
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
			if err != nil {
				return nil, fmt.Errorf("connect to %s (start trunk serve, or use search --local): %w", socket, err)
			}
			return conn, nil
		}
		endpoint = "http://unix"
	} else {
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" && u.Path != "/events" && u.Path != "/collect" {
			return fmt.Errorf("endpoint must be https://host[:port] or https://host[:port]/collect")
		}
		u.Path = ""
		endpoint = u.String()
	}
	h3 := &http3.Transport{TLSClientConfig: connection.TLS, QUICConfig: &quic.Config{HandshakeIdleTimeout: 2 * time.Second}}
	defer h3.Close()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if connection.Endpoint != "" && connection.Transport != "http2" {
		client.Transport = h3
	}
	request := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/collect?"+c.Query().Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", PIL)
		req.Header.Set("PIL-Version", "2")
		if connection.Endpoint != "" && connection.Token != "" {
			req.Header.Set("Authorization", "Bearer "+connection.Token)
		}
		return client.Do(req)
	}
	response, err := request()
	if err != nil && client.Transport == h3 && connection.Transport != "http3" && ctx.Err() == nil {
		client.Transport = transport
		response, err = request()
	}
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err := checkResponse(response, PIL); err != nil {
		return err
	}
	if connection.Endpoint != "" && response.ProtoMajor < 2 {
		return fmt.Errorf("remote search requires HTTP/2 or HTTP/3")
	}
	version, versionErr := hex.DecodeString(response.Header.Get("Definition-Version"))
	if response.Header.Get("PIL-Version") != "2" || response.Header.Get("Trunk-Completion") != "body" || versionErr != nil || len(version) != 32 {
		return fmt.Errorf("unsupported or incomplete PIL collection negotiation")
	}
	if c.Headers == nil {
		c.Headers = strings.Split(response.Header.Get("Trunk-Entry-Headers"), ",")
	}
	prefix := make([]byte, 5)
	if _, err := io.ReadFull(response.Body, prefix); err != nil {
		return fmt.Errorf("missing PIL stream header: %w", err)
	}
	if !bytes.Equal(prefix, []byte("TPIL\x02")) {
		return fmt.Errorf("unsupported PIL stream header")
	}
	input := &pilInput{Reader: io.MultiReader(bytes.NewReader(prefix), response.Body), body: response.Body}
	if err := DecodePIL(ctx, c, input, output); err != nil {
		return err
	}
	if response.Trailer.Get("Trunk-Status") != "ok" {
		return fmt.Errorf("search stream did not complete successfully")
	}
	return nil
}

type pilInput struct {
	io.Reader
	body io.Closer
}

func (p *pilInput) Close() error { return p.body.Close() }

func checkResponse(response *http.Response, contentType string) error {
	if response.StatusCode != http.StatusOK {
		message, err := io.ReadAll(io.LimitReader(response.Body, 8192))
		if err != nil {
			return err
		}
		return fmt.Errorf("search: %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	if response.Header.Get("Content-Type") != contentType {
		return fmt.Errorf("unexpected search content type: expected %s", contentType)
	}
	return nil
}
