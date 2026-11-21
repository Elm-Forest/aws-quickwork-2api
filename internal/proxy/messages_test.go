package proxy

import (
	"strings"
	"testing"
)

func TestSystemPromptAppendsUpstreamOverride(t *testing.T) {
	got := systemPrompt([]Message{
		{Role: "system", Content: "You are a coding agent."},
		{Role: "developer", Content: "Be terse."},
	})
	if !strings.HasPrefix(got, "You are a coding agent.\n\nBe terse.\n\n") {
		t.Fatalf("client instructions must come first:\n%s", got)
	}
	if !strings.HasSuffix(got, upstreamPromptOverride) {
		t.Fatalf("upstream override must be last:\n%s", got)
	}
}

func TestSystemPromptWithoutClientInstructions(t *testing.T) {
	got := systemPrompt([]Message{{Role: "user", Content: "hi"}})
	if got != upstreamPromptOverride {
		t.Fatalf("expected only the upstream override, got:\n%s", got)
	}
}
