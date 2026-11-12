package main

import (
	"strings"
	"testing"
)

func TestPostmortemPromptGroundsActionReferences(t *testing.T) {
	for _, required := range []string{"[action:ID]", "[context:ID]", "Никогда не выдумывай ID", "Если actions пуст", "не восстанавливай ни из context"} {
		if !strings.Contains(postmortemPrompt, required) {
			t.Errorf("embedded postmortem prompt lacks grounding rule %q", required)
		}
	}
}
