package vm

import "os"

// ProtectSSHKey applies owner-only permissions to a file before private key
// material is written. Both API connections and saved VM connections use it.
func ProtectSSHKey(file *os.File) error {
	return protectSSHKey(file)
}
