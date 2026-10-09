//go:build !darwin && !linux

package os

func QuietTerminal() func() { return func() {} }
