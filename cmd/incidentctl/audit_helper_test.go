package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Use a real Go child process to test the standalone LLM-client handoff.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "analyze" {
		if os.Getenv("INCIDENTCTL_TEST_AGENT_FAIL") == "1" {
			fmt.Fprintln(os.Stderr, "LLM HTTP 401: check API key")
			os.Exit(1)
		}
		args := map[string]string{}
		for i := 2; i+1 < len(os.Args); i += 2 {
			args[os.Args[i]] = os.Args[i+1]
		}
		check := func(ok bool) {
			if !ok {
				fmt.Fprintln(os.Stderr, "invalid process handoff")
				os.Exit(2)
			}
		}
		check(args["--config"] == "local-profile.json")
		b, e := os.ReadFile(args["--input"])
		check(e == nil)
		var packet map[string]any
		check(json.Unmarshal(b, &packet) == nil && packet["session"] == "attempt-1")
		b, e = os.ReadFile(args["--validation"])
		check(e == nil)
		var validation map[string]any
		check(json.Unmarshal(b, &validation) == nil && validation["passed"] == true)
		b, e = os.ReadFile(args["--task"])
		check(e == nil && string(b) == "Exercise goal")
		dir := args["--output"]
		check(os.Mkdir(dir, 0700) == nil)
		check(os.WriteFile(filepath.Join(dir, "analysis.md"), []byte("Analysis complete"), 0600) == nil)
		check(os.WriteFile(filepath.Join(dir, "analysis.json"), []byte("{}"), 0600) == nil)
		os.Exit(0)
	}
	os.Exit(m.Run())
}
