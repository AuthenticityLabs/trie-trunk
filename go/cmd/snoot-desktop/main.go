package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type configuration struct {
	Endpoint  string `json:"endpoint"`
	Socket    string `json:"socket,omitempty"`
	Port      int    `json:"port"`
	Transport string `json:"transport,omitempty"`
}

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	resources := filepath.Join(filepath.Dir(exe), "..", "Resources")
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	defaultState := filepath.Join(home, "Library", "Application Support", "Snoot")
	state := flag.String("state-dir", defaultState, "profile configuration directory")
	endpoint := flag.String("endpoint", "", "HTTP(S) server origin override")
	socket := flag.String("socket", "", "Unix socket override; prefer when available")
	transportMode := flag.String("transport", "", "transport override: auto, socket, or http")
	port := flag.Int("port", 0, "stable loopback port override")
	root := flag.String("resources", resources, "application resource directory")
	headless := flag.Bool("bridge-only", false, "run only the local frontend/transport bridge")
	selfTest := flag.Bool("self-test", false, "verify native WebKit features and frontend, then exit")
	dashboardTest := flag.Bool("check-dashboard", false, "check the existing profile dashboard and report loaded assets, then exit")
	inspector := flag.Bool("inspect", false, "enable WebKit developer tools")
	flag.Parse()
	if err = os.MkdirAll(*state, 0700); err != nil {
		return err
	}
	cfg := configuration{Endpoint: "https://snoot.trietrunk.com", Port: 34185}
	configPath := filepath.Join(*state, "connection.json")
	raw, err := os.ReadFile(configPath)
	if err == nil {
		if err = json.Unmarshal(raw, &cfg); err != nil {
			return fmt.Errorf("connection.json: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if *endpoint != "" {
		cfg.Endpoint = *endpoint
	}
	if *socket != "" {
		cfg.Socket = *socket
	}
	if *transportMode != "" {
		cfg.Transport = *transportMode
	}
	if cfg.Transport == "" {
		cfg.Transport = "auto"
	}
	if !validTransport(cfg.Transport) {
		return fmt.Errorf("transport must be auto, socket, or http")
	}
	if *port != 0 {
		cfg.Port = *port
	}
	if cfg.Socket == "" {
		cfg.Socket = os.Getenv("SNOOT_SOCKET")
		if cfg.Socket == "" {
			cfg.Socket = os.Getenv("TRUNK_SOCKET")
		}
		if cfg.Socket == "" && os.Getenv("TRUNK_DIR") != "" {
			cfg.Socket, err = filepath.Abs(filepath.Join(os.Getenv("TRUNK_DIR"), ".trunk", "search.sock"))
			if err != nil {
				return err
			}
		}
	}
	if cfg.Port < 1024 || cfg.Port > 65535 {
		return fmt.Errorf("port must be between 1024 and 65535")
	}
	if cfg.Socket != "" {
		if strings.HasPrefix(cfg.Socket, "~/") {
			cfg.Socket = filepath.Join(home, cfg.Socket[2:])
		}
		if !filepath.IsAbs(cfg.Socket) {
			return fmt.Errorf("socket path must be absolute")
		}
	}
	if _, err = os.Stat(configPath); os.IsNotExist(err) {
		if err = saveConfiguration(configPath, cfg); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	target, transport, mode, err := configuredTransport(ctx, cfg)
	if err != nil {
		return err
	}
	defer transport.CloseIdleConnections()
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port)))
	if err != nil {
		return fmt.Errorf("Snoot's local port is unavailable (another Snoot window may be open): %w", err)
	}
	var secret [32]byte
	if _, err = rand.Read(secret[:]); err != nil {
		listener.Close()
		return err
	}
	token := hex.EncodeToString(secret[:])
	host := listener.Addr().String()
	app := newBridge(host, token, os.DirFS(filepath.Join(*root, "web")), target, transport, mode)
	app.connection = &desktopConnection{cfg: cfg, path: configPath, activePreference: cfg.Transport}
	app.selfTest = *selfTest || *dashboardTest
	app.dashboardTest = *dashboardTest
	server := &http.Server{Handler: app, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute, BaseContext: func(net.Listener) context.Context { return ctx }}
	failure := make(chan error, 1)
	go func() { failure <- server.Serve(listener) }()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	log.Printf("Snoot listening on http://%s using %s", host, mode)
	if *headless {
		select {
		case <-ctx.Done():
			return nil
		case e := <-failure:
			return e
		}
	}
	runtime := filepath.Join(filepath.Dir(exe), "neutralino")
	args := []string{"--path=" + *root, "--res-mode=directory", "--url=http://" + host + "/?desktop-session=" + token, "--window-enable-inspector=" + strconv.FormatBool(*inspector)}
	cmd := exec.CommandContext(ctx, runtime, args...)
	cmd.Dir = *state
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
		close(exited)
	}()
	defer func() {
		stop()
		select {
		case <-exited:
		case <-time.After(time.Second):
		}
	}()
	if *selfTest || *dashboardTest {
		select {
		case report := <-app.report:
			var result struct {
				OK bool `json:"ok"`
			}
			json.Unmarshal(report, &result)
			fmt.Println(string(report))
			if err = os.WriteFile(filepath.Join(*state, "self-test.json"), report, 0600); err != nil {
				return err
			}
			if !result.OK {
				return errors.New("native WebKit self-test failed")
			}
			return nil
		case <-time.After(60 * time.Second):
			return errors.New("native WebKit self-test timed out")
		case e := <-exited:
			return fmt.Errorf("Neutralino exited before self-test: %v", e)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case e := <-exited:
		return e
	case e := <-failure:
		return e
	case <-ctx.Done():
		return nil
	}
}
