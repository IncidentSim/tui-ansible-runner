//go:build !windows

package main

import "strings"

func localSSHCommand(args []string) string {
	parts := make([]string, len(args))
	for i, arg := range args {
		parts[i] = localShellQuote(arg)
	}
	return strings.Join(parts, " ")
}
