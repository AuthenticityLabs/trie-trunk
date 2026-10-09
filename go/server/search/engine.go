package search

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Engine owns one resident Rust reader. The IPC transports encoded output bytes;
// event values are never decoded/re-encoded by this adapter.
type Engine struct {
	mu      sync.Mutex
	root    string
	workers int
	dir     string
	socket  string
	cmd     *exec.Cmd
	done    chan error
	control io.WriteCloser
	closed  bool
}

func NewEngine(root string, workers int) *Engine { return &Engine{root: root, workers: workers} }
func (e *Engine) start() (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return "", fmt.Errorf("search engine closed")
	}
	if e.cmd != nil {
		select {
		case <-e.done:
			if e.control != nil {
				e.control.Close()
			}
			os.RemoveAll(e.dir)
			e.cmd = nil
		default:
			return e.socket, nil
		}
	}
	binary, err := binaryPath()
	if err != nil {
		return "", err
	}
	// Unix socket addresses have short platform limits. This directory is private.
	dir, err := os.MkdirTemp("/tmp", "trunk-reader-")
	if err != nil {
		return "", err
	}
	socket := filepath.Join(dir, "reader.sock")
	cmd := exec.Command(binary, "--worker", e.root, socket, strconv.Itoa(e.workers))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	control, err := cmd.StdinPipe()
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		control.Close()
		os.RemoveAll(dir)
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err == nil && line != "READY\n" {
			err = fmt.Errorf("unexpected reader startup response")
		}
		ready <- err
	}()
	select {
	case err = <-ready:
	case <-time.After(10 * time.Second):
		err = fmt.Errorf("reader startup timed out")
	}
	if err != nil {
		control.Close()
		_ = cmd.Process.Kill()
		<-done
		os.RemoveAll(dir)
		return "", err
	}
	e.dir = dir
	e.socket = socket
	e.cmd = cmd
	e.done = done
	e.control = control
	return socket, nil
}
func (e *Engine) Run(ctx context.Context, c Config, output io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if c.Root != e.root {
		return fmt.Errorf("search engine root mismatch")
	}
	request, err := json.Marshal(arguments(c))
	if err != nil {
		return err
	}
	if len(request) > 65536 {
		return fmt.Errorf("search request too large")
	}
	socket, err := e.start()
	if err != nil {
		return err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	err = runIPC(conn, request, output)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
func runIPC(conn net.Conn, request []byte, output io.Writer) error {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(request)))
	if _, err := conn.Write(append(header[:], request...)); err != nil {
		return err
	}
	var buffer [65536]byte
	for {
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			return fmt.Errorf("resident reader: %w", err)
		}
		n := binary.BigEndian.Uint32(header[:])
		if n == 0 {
			return nil
		}
		failed := n == ^uint32(0)
		if failed {
			if _, err := io.ReadFull(conn, header[:]); err != nil {
				return err
			}
			n = binary.BigEndian.Uint32(header[:])
		}
		if n > uint32(len(buffer)) {
			return fmt.Errorf("invalid reader chunk size")
		}
		if _, err := io.ReadFull(conn, buffer[:n]); err != nil {
			return err
		}
		if failed {
			return fmt.Errorf("resident reader: %s", buffer[:n])
		}
		if _, err := output.Write(buffer[:n]); err != nil {
			return err
		}
	}
}
func (e *Engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	if e.cmd != nil {
		if e.control != nil {
			e.control.Close()
		}
		_ = e.cmd.Process.Kill()
		<-e.done
		e.cmd = nil
		os.RemoveAll(e.dir)
	}
}
