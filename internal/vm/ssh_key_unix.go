//go:build !windows

package vm

import "os"

func protectSSHKey(file *os.File) error {
	return file.Chmod(0o600)
}
