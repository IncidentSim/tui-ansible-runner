package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"incidentctl/internal/config"
)

func TestInlineLLMKeyOnlyPassedToLocalChild(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy.invalid")
	t.Setenv("DEEPSEEK_API_KEY", "environment-test-key")
	a := defaultAuditOptions()
	a.LLM = &config.LLMConfig{Enabled: true, APIKey: "config-test-key", APIKeyEnv: "DEEPSEEK_API_KEY", BaseURL: "https://api.deepseek.com", Model: "deepseek-flash", TimeoutSeconds: 120, MaxTokens: 4000, ProxyMode: "direct"}
	path, env, cleanup, err := a.analysisSettings()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "config-test-key") || strings.Contains(string(data), "environment-test-key") {
		t.Fatal("API key persisted in temporary profile")
	}
	var profile map[string]any
	if err := json.Unmarshal(data, &profile); err != nil {
		t.Fatal(err)
	}
	prompt, err := os.ReadFile(filepath.Join(filepath.Dir(path), profile["prompt_file"].(string)))
	if err != nil || !strings.Contains(string(prompt), "Постмортем") {
		t.Fatal("embedded postmortem prompt missing")
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "INCIDENTCTL_LLM_API_KEY=config-test-key") {
		t.Fatal("configured key not passed to child")
	}
	if strings.Contains(joined, "proxy.invalid") || !strings.Contains(joined, "NO_PROXY=*") {
		t.Fatal("direct mode did not bypass proxy in child")
	}
	if os.Getenv("HTTP_PROXY") != "http://proxy.invalid" || os.Getenv("INCIDENTCTL_LLM_API_KEY") != "" {
		t.Fatal("analysis modified parent environment")
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("temporary profile not cleaned up")
	}
}

func TestInlineLLMEnvironmentKeyAndDisabledMode(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "environment-test-key")
	a := defaultAuditOptions()
	a.LLM = &config.LLMConfig{Enabled: true, APIKeyEnv: "DEEPSEEK_API_KEY"}
	_, env, cleanup, err := a.analysisSettings()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !strings.Contains(strings.Join(env, "\n"), "INCIDENTCTL_LLM_API_KEY=environment-test-key") {
		t.Fatal("environment key not used")
	}
	a.LLM.Enabled = false
	a.LLMConfig = "legacy-profile.json"
	if a.llmEnabled() {
		t.Fatal("disabled inline profile must override legacy settings")
	}
}
