package main

import (
	"strings"
	"syscall"
)

// Stop PowerShell parsing so it preserves the quotes inside ProxyCommand.
func localSSHCommand(args []string) string {
	parts := []string{args[0], "--%"}
	for _, arg := range args[1:] {
		parts = append(parts, syscall.EscapeArg(arg))
	}
	return strings.Join(parts, " ")
}
