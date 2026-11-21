package proxy

import (
	"encoding/json"
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

func TestSystemPromptForwardsToolDefinitionsVerbatim(t *testing.T) {
	req := &ChatRequest{
		Tools: []Tool{{
			Type: "function",
			Function: ToolFunction{
				Name:        "read_file",
				Description: "Read a file",
				Parameters:  map[string]any{"type": "object"},
			},
		}},
		ToolChoice: json.RawMessage(`"auto"`),
	}
	extra := forwardToolDefinitions(req)
	for _, want := range []string{`"read_file"`, `"Read a file"`, `"tool_choice"`, `"auto"`} {
		if !strings.Contains(extra, want) {
			t.Fatalf("forwarded definitions missing %s:\n%s", want, extra)
		}
	}
	got := systemPrompt(nil, extra)
	if strings.Index(got, "forwarded verbatim") > strings.Index(got, upstreamPromptOverride) {
		t.Fatalf("upstream override must come after forwarded definitions:\n%s", got)
	}
}
