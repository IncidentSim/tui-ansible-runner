package main

import (
	"archive/tar"
	"bytes"
	"debug/elf"
	"encoding/json"
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

const (
	packagedActionPacket  = `{"v":1,"session":"attempt-1","actions":[{"id":1,"cmd":"systemctl status app","t":1,"rc":0}],"context":[{"id":2,"cmd":"background"}]}`
	packagedEmptyPacket   = `{"v":1,"session":"attempt-1","actions":[]}`
	packagedContextPacket = `{"v":1,"session":"attempt-1","actions":[],"context":[{"id":2,"cmd":"background"}]}`
)

// runPackagedAnalyze drives the real packaged native agent through verify with a
// loopback-only fake provider. It returns the provider call count, analysis.md
// and the decoded analysis.json.
func runPackagedAnalyze(t *testing.T, packet, answer string) (int, string, map[string]any) {
	t.Helper()
	if _, e := auditbinary.Native(); e != nil {
		t.Skip(e)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer local-test-key" {
			t.Error("incorrect LLM request")
		}
		io.WriteString(w, `{"model":"local-test","id":"resp-1","choices":[{"message":{"content":`+strconvQuote(answer)+`},"finish_reason":"stop"}],"usage":{"prompt_tokens":10}}`)
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
	if e := a.verify(&fakeAuditClient{packet: packet}, "/work", "example", task, &output, func(io.Writer) error { return nil }); e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(a.LLMReportsDir, "example", "1")
	md, e := os.ReadFile(filepath.Join(dir, "analysis.md"))
	if e != nil {
		t.Fatalf("Go agent failed: %v / %s", e, output.String())
	}
	raw, e := os.ReadFile(filepath.Join(dir, "analysis.json"))
	if e != nil {
		t.Fatal(e)
	}
	var report map[string]any
	if e := json.Unmarshal(raw, &report); e != nil {
		t.Fatal(e)
	}
	return calls, string(md), report
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestPackagedGoAgentLLMEndToEnd(t *testing.T) {
	const answer = "Постмортем готов: см. [action:1]"
	calls, md, report := runPackagedAnalyze(t, packagedActionPacket, answer)
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
	facts := "- [action:1] t=1с rc=0 `systemctl status app`"
	explanation := "## Объяснение модели"
	for _, want := range []string{"Достоверные данные (сформированы программой, не моделью)", "Действий участника: 1", "Записей context (авторство не установлено): 1", facts, explanation, answer} {
		if !strings.Contains(md, want) {
			t.Errorf("analysis.md lacks %q:\n%s", want, md)
		}
	}
	if !(strings.Index(md, facts) < strings.Index(md, explanation) && strings.Index(md, explanation) < strings.Index(md, answer)) {
		t.Errorf("program facts must precede the labelled model text:\n%s", md)
	}
	if strings.Contains(md, "background") {
		t.Errorf("context telemetry must not be rendered as participant history:\n%s", md)
	}
	if report["llm_skipped"] != nil || report["explanation"] != answer || report["response_id"] != "resp-1" {
		t.Errorf("unexpected analysis.json: %v", report)
	}
	if analysis, _ := report["analysis"].(string); strings.TrimSpace(analysis) != strings.TrimSpace(md) {
		t.Errorf("analysis.json analysis differs from analysis.md")
	}
}

func TestPackagedGoAgentSkipsProviderWithoutActions(t *testing.T) {
	for name, packet := range map[string]string{"empty": packagedEmptyPacket, "context-only": packagedContextPacket} {
		t.Run(name, func(t *testing.T) {
			// If the agent wrongly called the provider, this text would be published.
			calls, md, report := runPackagedAnalyze(t, packet, "Выдуманное объяснение [action:1]")
			if calls != 0 {
				t.Fatalf("provider calls = %d, want 0", calls)
			}
			for _, want := range []string{"**Записанных действий участника нет.**", "Действий участника: 0", "LLM не вызывалась"} {
				if !strings.Contains(md, want) {
					t.Errorf("analysis.md lacks %q:\n%s", want, md)
				}
			}
			for _, bad := range []string{"Выдуманное объяснение", "## Объяснение модели", "background"} {
				if strings.Contains(md, bad) {
					t.Errorf("analysis.md must not contain %q:\n%s", bad, md)
				}
			}
			if report["llm_skipped"] != true {
				t.Errorf("llm_skipped = %v, want true", report["llm_skipped"])
			}
			for _, k := range []string{"explanation", "model", "usage", "response_id", "request_sha256"} {
				if _, ok := report[k]; ok {
					t.Errorf("analysis.json must not claim a provider call: has %q", k)
				}
			}
		})
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
