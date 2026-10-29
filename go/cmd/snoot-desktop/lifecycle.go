package main

import (
	"os/exec"
	"syscall"
	"time"
)

// Let the Cocoa main queue save window state and close WebKit. CommandContext's
// default SIGKILL skips that cleanup. WaitDelay still bounds an unresponsive
// runtime so the bridge cannot keep the local port occupied indefinitely.
func configureRuntimeShutdown(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
}
