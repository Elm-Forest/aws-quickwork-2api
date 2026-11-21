package proxy

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Message is an OpenAI chat message. Content is decoded as any because the
// OpenAI schema allows both a string and an array of typed parts.
type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
	N        int       `json:"n"`
}

// uuid returns a random RFC 4122 v4 UUID.
func uuid() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]))
}

// contentText flattens the OpenAI content field, which may be a plain string
// or an array of typed parts.
func contentText(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var b strings.Builder
		for _, part := range c {
			m, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := m["text"].(string); ok {
				b.WriteString(t)
			}
		}
		return b.String()
	case nil:
		return ""
	default:
		body, err := json.Marshal(c)
		if err != nil {
			return ""
		}
		return string(body)
	}
}

// formatPrompt flattens a chat transcript into the single text field expected
// by QuickWork's user_message frame. A lone user message is passed through
// unchanged so the upstream agent sees the prompt exactly as typed.
func formatPrompt(msgs []Message) (string, error) {
	if len(msgs) == 0 {
		return "", fmt.Errorf("messages must not be empty")
	}
	nonEmpty := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		text := strings.TrimSpace(contentText(m.Content))
		if text == "" {
			continue
		}
		m.Content = text
		nonEmpty = append(nonEmpty, m)
	}
	if len(nonEmpty) == 0 {
		return "", fmt.Errorf("messages contain no text")
	}
	if len(nonEmpty) == 1 && nonEmpty[0].Role == "user" {
		return nonEmpty[0].Content.(string), nil
	}
	var b strings.Builder
	for i, m := range nonEmpty {
		if i > 0 {
			b.WriteString("\n\n")
		}
		role := m.Role
		if role == "" {
			role = "user"
		}
		b.WriteString(strings.ToUpper(role[:1]) + role[1:])
		b.WriteString(": ")
		b.WriteString(m.Content.(string))
	}
	return b.String(), nil
}
