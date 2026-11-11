package vm

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSSHKeyWindowsACLAndOpenSSH(t *testing.T) {
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("OpenSSH ssh-keygen is unavailable")
	}
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	key := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	file, err := os.CreateTemp(t.TempDir(), "temporary key-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if err := protectSSHKey(file); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(key); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.GetNamedSecurityInfo(file.Name(), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	want := "D:P(A;;FA;;;" + user.User.Sid.String() + ")"
	if descriptor.String() != want {
		t.Fatalf("key ACL = %s, want %s", descriptor.String(), want)
	}
	// Use Windows OpenSSH itself to verify that the saved private key is accepted.
	output, err := exec.Command(keygen, "-y", "-f", file.Name()).CombinedOutput()
	if err != nil {
		t.Fatalf("OpenSSH rejected protected key: %v: %s", err, output)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(output)), "ssh-rsa ") {
		t.Fatal("OpenSSH did not read the private key")
	}
}
