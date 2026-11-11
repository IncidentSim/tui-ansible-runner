package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

//go:embed prompts/postmortem.md
var postmortemPrompt string

func (a auditOptions) agentDirectory() (string, error) {
	if a.AgentDir != "" {
		return a.AgentDir, nil
	}
	if a.Source == nil {
		return "", errors.New("audit-agent repository is not configured")
	}
	return a.Source.Directory()
}

func (a auditOptions) llmEnabled() bool {
	if a.LLM != nil {
		return a.LLM.Enabled
	}
	return a.LLMConfig != ""
}

// The temporary Python profile never contains the API key. Only the local
// analysis child receives it; the SSH client and the guest receive no key.
func (a auditOptions) analysisSettings() (string, []string, func(), error) {
	if a.LLM == nil {
		return a.LLMConfig, os.Environ(), func() {}, nil
	}
	key := a.LLM.APIKey
	if key == "" {
		key = os.Getenv(a.LLM.APIKeyEnv)
	}
	if key == "" {
		return "", nil, nil, errors.New("DeepSeek API key is missing; set audit.llm.api_key or its api_key_env environment variable")
	}
	folder, err := os.MkdirTemp("", "incidentctl-llm-*")
	if err != nil {
		return "", nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(folder) }
	if err := os.WriteFile(filepath.Join(folder, "postmortem.md"), []byte(postmortemPrompt), 0600); err != nil {
		cleanup()
		return "", nil, nil, err
	}
	profile := map[string]any{
		"active_profile": "deepseek", "prompt_file": "postmortem.md",
		"max_request_bytes": 262144, "max_response_bytes": 2097152,
		"profiles": map[string]any{"deepseek": map[string]any{
			"adapter": "chat_completions", "base_url": a.LLM.BaseURL, "model": a.LLM.Model,
			"api_key_env": "INCIDENTCTL_LLM_API_KEY", "timeout_seconds": a.LLM.TimeoutSeconds,
			"proxy_mode": a.LLM.ProxyMode,
			"parameters": map[string]any{"max_tokens": a.LLM.MaxTokens, "thinking": map[string]string{"type": "disabled"}},
		}},
	}
	data, err := json.Marshal(profile)
	if err != nil {
		cleanup()
		return "", nil, nil, err
	}
	path := filepath.Join(folder, "profile.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		cleanup()
		return "", nil, nil, err
	}
	env := analysisEnvironment(os.Environ(), key, a.LLM.ProxyMode == "direct")
	return path, env, cleanup, nil
}

func analysisEnvironment(base []string, key string, direct bool) []string {
	env := make([]string, 0, len(base)+3)
	for _, item := range base {
		name, _, _ := strings.Cut(item, "=")
		switch strings.ToUpper(name) {
		case "INCIDENTCTL_LLM_API_KEY":
			continue
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY":
			if direct {
				continue
			}
		}
		env = append(env, item)
	}
	env = append(env, "INCIDENTCTL_LLM_API_KEY="+key)
	// Also works with older versions of the downloaded Python client, including
	// Windows registry proxies. This setting applies only to the local child.
	if direct {
		env = append(env, "NO_PROXY=*", "no_proxy=*")
	}
	return env
}
