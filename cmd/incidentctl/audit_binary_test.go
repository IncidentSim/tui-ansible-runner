package main

import (
	"archive/tar"
	"bytes"
	"debug/elf"
	"incidentctl/internal/auditbinary"
	"incidentctl/internal/config"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackagedGoAgentLLMEndToEnd(t *testing.T) {
	if _, e := auditbinary.Native(); e != nil {
		t.Skip(e)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer local-test-key" {
			t.Error("incorrect LLM request")
		}
		io.WriteString(w, `{"model":"local-test","choices":[{"message":{"content":"Постмортем готов"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10}}`)
	}))
	defer server.Close()
	a := defaultAuditOptions()
	a.ReportsDir = t.TempDir()
	a.LLMReportsDir = t.TempDir()
	a.LLM = &config.LLMConfig{Enabled: true, APIKey: "local-test-key", APIKeyEnv: "DEEPSEEK_API_KEY", BaseURL: server.URL, Model: "test", TimeoutSeconds: 2, MaxTokens: 100, ProxyMode: "direct"}
	task := t.TempDir()
	if e := os.WriteFile(filepath.Join(task, "README.md"), []byte("Repair service"), 0600); e != nil {
		t.Fatal(e)
	}
	// All executables are packaged: Python and Go are absent from the child's PATH.
	t.Setenv("PATH", t.TempDir())
	var output bytes.Buffer
	if e := a.verify(&fakeAuditClient{packet: `{"v":1,"session":"attempt-1","actions":[]}`}, "/work", "example", task, &output, func(io.Writer) error { return nil }); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(a.LLMReportsDir, "example", "1", "analysis.md"))
	if e != nil || strings.TrimSpace(string(b)) != "Постмортем готов" || calls != 1 {
		t.Fatalf("Go agent failed: %v %s / %s", e, b, output.String())
	}
}
func TestPackagedGuestIsLinuxExecutable(t *testing.T) {
	b, e := auditbinary.Read("linux", "amd64")
	if e != nil {
		t.Skip(e)
	}
	if len(b) < 20 || !bytes.Equal(b[:4], []byte{127, 'E', 'L', 'F'}) || b[18] != 62 {
		t.Fatal("wrong guest architecture")
	}
	guest, err := elf.NewFile(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer guest.Close()
	for _, program := range guest.Progs {
		if program.Type == elf.PT_INTERP {
			t.Fatal("guest binary requires a dynamic loader")
		}
	}
	root := t.TempDir()
	path := filepath.Join(root, "bin", "linux-amd64", "incident-audit")
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, b, 0600)
	bundle, e := auditBundle(root)
	if e != nil {
		t.Fatal(e)
	}
	r := tar.NewReader(bytes.NewReader(bundle))
	header, e := r.Next()
	if e != nil || header.Name != "incident-audit" || header.Mode != 0700 {
		t.Fatal(header, e)
	}
	payload, _ := io.ReadAll(r)
	if !bytes.Equal(payload, b) {
		t.Fatal("guest upload changed executable")
	}
	if _, e = r.Next(); e != io.EOF {
		t.Fatal("unexpected extra files")
	}
}
