//go:build darwin || linux

package os

import (
	"os"

	"golang.org/x/sys/unix"
)

func QuietTerminal() func() {
	terminal, err := os.Open("/dev/tty")
	if err != nil {
		return func() {}
	}
	closeTerminal := func() { terminal.Close() }
	fd := int(terminal.Fd())
	foreground, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	if err != nil || foreground != unix.Getpgrp() {
		return closeTerminal
	}
	original, err := unix.IoctlGetTermios(fd, getTermios)
	if err != nil || original.Lflag&unix.ECHOCTL == 0 {
		return closeTerminal
	}
	quiet := *original
	quiet.Lflag &^= unix.ECHOCTL
	if err := unix.IoctlSetTermios(fd, setTermios, &quiet); err != nil {
		return closeTerminal
	}
	return func() {
		unix.IoctlSetTermios(fd, setTermios, original)
		terminal.Close()
	}
}
