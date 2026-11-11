package vm

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// Chmod does not restrict NTFS ACLs. Remove inherited permissions and grant
// only the current user access before writing any private key material.
func protectSSHKey(file *os.File) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("get Windows user SID: %w", err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return fmt.Errorf("create private key ACL: %w", err)
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read private key ACL: %w", err)
	}
	return windows.SetNamedSecurityInfo(file.Name(), windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil)
}
