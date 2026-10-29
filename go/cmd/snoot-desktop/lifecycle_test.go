package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeCancellationAllowsCleanup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "closed")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `trap 'echo closed > "$1"; exit 0' TERM; echo ready; while :; do sleep 0.1; done`, "runtime", marker)
	configureRuntimeShutdown(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	ready := make(chan bool, 1)
	go func() { ready <- bufio.NewScanner(stdout).Scan() }()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("runtime did not start")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runtime startup timed out")
	}
	cancel()
	if err := cmd.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("shutdown skipped cleanup: %v", err)
	}
}

func TestRuntimeCancellationBoundsUnresponsiveProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `trap '' TERM; echo ready; exec sleep 60`)
	configureRuntimeShutdown(cmd)
	cmd.WaitDelay = 100 * time.Millisecond
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	if !bufio.NewScanner(stdout).Scan() {
		t.Fatal("runtime did not start")
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unresponsive runtime unexpectedly exited successfully")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runtime was not stopped within the deadline")
	}
}
