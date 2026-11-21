package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"quickwork-openai/internal/config"
)

const (
	defaultWSPath        = "/streaming/quickwork-chat-bidirectional-stream"
	defaultGatewayWSPath = "/ws/service/SpaceNeedleConversationService/operation/ChatBidirectionalStream"
	maxUserMessageBytes  = 63 * 1024
)

// ErrUserMessageTooLarge means the user_message frame cannot fit under the
// upstream 64 KB per-frame limit even after trimming older prompt text.
var ErrUserMessageTooLarge = errors.New("user_message exceeds the upstream 64 KB frame limit")

type Upstream struct {
	cfg *config.Config
}

func NewUpstream(cfg *config.Config) *Upstream {
	return &Upstream{cfg: cfg}
}

// ChatInput is one upstream turn.
type ChatInput struct {
	Prompt       string
	Model        string
	ThreadID     string
	Title        string
	SystemPrompt string
}

// ToolCallState is a native tool invocation reported by the QuickWork agent.
type ToolCallState struct {
	ID        string
	Name      string
	Arguments string
}

// Usage aggregates turn_usage events across a multi-step turn.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CacheReadTokens  int
	CacheWriteTokens int
}

func (u *Usage) add(v Usage) {
	u.PromptTokens += v.PromptTokens
	u.CompletionTokens += v.CompletionTokens
	u.TotalTokens += v.TotalTokens
	u.CacheReadTokens += v.CacheReadTokens
	u.CacheWriteTokens += v.CacheWriteTokens
}

// StreamEvent is a text delta or a native tool call from the upstream agent.
type StreamEvent struct {
	Type     string
	Text     string
	ToolCall *ToolCallState
}

type EventFunc func(StreamEvent) error

type ChatResult struct {
	Text      string
	ToolCalls []ToolCallState
	Usage     Usage
}

// Chat sends one prompt through the QuickWork chat bidi WebSocket. Text
// deltas are reported through onEvent.
func (u *Upstream) Chat(ctx context.Context, sess *Session, in ChatInput, onEvent EventFunc) (ChatResult, error) {
	if strings.TrimSpace(in.Prompt) == "" {
		return ChatResult{}, fmt.Errorf("prompt is empty")
	}
	threadID := strings.TrimSpace(in.ThreadID)
	if threadID == "" {
		threadID = uuid()
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = titleFor(in.Prompt)
	}
	messageID := uuid()
	traceID := uuid()

	wsURL, headers, gateway, err := u.dialInfo(sess)
	if err != nil {
		return ChatResult{}, err
	}

	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 20 * time.Second
	conn, resp, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		detail := ""
		if resp != nil {
			detail = fmt.Sprintf(" HTTP %d", resp.StatusCode)
		}
		return ChatResult{}, fmt.Errorf("upstream WebSocket dial failed%s: %w", detail, err)
	}
	defer conn.Close()

	writeMu := &sync.Mutex{}
	encode := func(v any) ([]byte, error) {
		body, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		if gateway {
			frame := gatewayEncode(string(body))
			if frame == nil {
				return nil, fmt.Errorf("encode gateway frame")
			}
			return frame, nil
		}
		// AIDP (enterprise and social) uses the block-style binary envelope:
		// {"eventType":"rawInputEvent","payload":{"payload":base64(json)}}.
		return aidpEncode(string(body)), nil
	}
	messageType := websocket.BinaryMessage
	send := func(v any) error {
		frame, err := encode(v)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteMessage(messageType, frame)
	}

	// Keep the socket alive while the agent is thinking.
	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopPing:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				writeMu.Lock()
				_ = conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(5*time.Second))
				writeMu.Unlock()
			}
		}
	}()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	// Handshake. The AIDP and Gateway paths use different session_connect shapes.
	if gateway {
		_ = send(map[string]any{"type": "session_connect", "session_id": sess.OutpostID})
	} else {
		_ = send(map[string]any{
			"type": "session_connect",
			"payload": map[string]any{
				"actor_id":               sess.ActorARN,
				"surfaceType":            sess.Surface,
				"outpostId":              sess.OutpostID,
				"appVersion":             u.cfg.AppVersion,
				"external_agent_sign_in": 1,
				"recents_promotion":      1,
			},
		})
	}
	_ = send(map[string]any{"type": "federate_status", "payload": map[string]any{}})
	_ = send(map[string]any{
		"type":    "session_create",
		"payload": map[string]any{"session_id": threadID, "title": title},
	})

	payload := map[string]any{
		"text":               in.Prompt,
		"send_origin":        "programmatic",
		"had_physical_input": false,
		"surfaceType":        sess.Surface,
		"outpostId":          sess.OutpostID,
		"appVersion":         u.cfg.AppVersion,
		"traceId":            traceID,
		"message_id":         messageID,
		"thread_id":          threadID,
		"agent_mode":         in.Model,
		"user_timezone":      u.cfg.Timezone,
		"user_locale":        u.cfg.Locale,
		"chat_agent_id":      nil,
		"auto_routing":       false,
		"web_search_enabled": false,
		"pre_approved_tools": []any{},
		"preload_skills":     []any{},
		"panel_edits":        []any{},
		"custom_prompt":      nil,
	}
	// system_prompt is the native per-turn override. It always carries the
	// caller's system/developer instructions plus the backend-prompt override
	// notice, so instructions the service attaches to the turn lose precedence.
	if strings.TrimSpace(in.SystemPrompt) != "" {
		payload["system_prompt"] = in.SystemPrompt
	}
	if u.cfg.ThinkingEffort != "" {
		payload["thinking_effort"] = u.cfg.ThinkingEffort
	}
	if err := fitUserMessage(payload); err != nil {
		return ChatResult{}, err
	}
	if err := send(map[string]any{"type": "user_message", "payload": payload}); err != nil {
		return ChatResult{}, fmt.Errorf("send user_message: %w", err)
	}

	result, err := u.readEvents(ctx, conn, gateway, threadID, onEvent)
	if err != nil {
		return result, err
	}

	if !u.cfg.KeepSessions {
		_ = send(map[string]any{"type": "session_delete", "payload": map[string]any{"session_id": threadID}})
	}
	return result, nil
}

func (u *Upstream) dialInfo(sess *Session) (wsURL string, headers http.Header, gateway bool, err error) {
	jwt := sess.jwt()
	if jwt == "" {
		return "", nil, false, fmt.Errorf("missing jwt")
	}
	gateway = isGatewayProvider(sess.Provider)

	host := strings.TrimSpace(sess.WSHost)
	path := strings.TrimSpace(sess.WSPath)
	if path == "" {
		if gateway {
			path = defaultGatewayWSPath
		} else {
			path = defaultWSPath
		}
	}
	switch {
	case strings.HasPrefix(host, "wss://") || strings.HasPrefix(host, "ws://"):
		host = strings.TrimRight(host, "/")
	case strings.HasPrefix(host, "https://"):
		host = "wss://" + strings.TrimPrefix(host, "https://")
	default:
		host = "wss://" + strings.TrimRight(host, "/")
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		wsURL = path
	} else {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		wsURL = host + path
	}

	parsed, err := url.Parse(wsURL)
	if err != nil {
		return "", nil, false, fmt.Errorf("invalid ws_url: %w", err)
	}
	if !gateway {
		q := parsed.Query()
		if sess.Surface != "" {
			q.Set("surfaceType", sess.Surface)
		}
		if sess.OutpostID != "" {
			q.Set("outpostId", sess.OutpostID)
		}
		if sess.WorkingSessionID != "" {
			q.Set("workingSessionId", sess.WorkingSessionID)
		}
		// Social AIDP transports accept the bearer in the query; enterprise
		// relies on the Authorization header injected below.
		if strings.EqualFold(sess.Provider, "social") {
			q.Set("token", jwt)
		}
		parsed.RawQuery = q.Encode()
	}

	headers = http.Header{}
	headers.Set("Authorization", "Bearer "+jwt)
	headers.Set("Origin", "file://")
	headers.Set("X-Surface-Type", sess.Surface)
	headers.Set("X-Outpost-Id", sess.OutpostID)
	headers.Set("X-Agent-Service-Type", "UNIFIED_QUICK")
	if sess.WorkingSessionID != "" {
		headers.Set("X-Working-Session-Id", sess.WorkingSessionID)
	}
	return parsed.String(), headers, gateway, nil
}

func (u *Upstream) readEvents(ctx context.Context, conn *websocket.Conn, gateway bool, threadID string, onEvent EventFunc) (ChatResult, error) {
	var result ChatResult
	var text strings.Builder
	sawMessageComplete := false
	var completeDeadline time.Time
	eventCounts := make(map[string]int)

	toolCalls := make(map[string]*ToolCallState)
	toolOrder := make([]string, 0, 2)
	toolSent := make(map[string]bool)

	getTool := func(id, name string) *ToolCallState {
		id = strings.TrimSpace(id)
		if id == "" {
			id = "call_" + strings.ReplaceAll(uuid(), "-", "")
		}
		tc := toolCalls[id]
		if tc == nil {
			tc = &ToolCallState{ID: id}
			toolCalls[id] = tc
			toolOrder = append(toolOrder, id)
		}
		if n := strings.TrimSpace(name); n != "" {
			tc.Name = n
		}
		return tc
	}
	emitTool := func(tc *ToolCallState) error {
		if tc == nil || onEvent == nil || toolSent[tc.ID] {
			return nil
		}
		toolSent[tc.ID] = true
		copy := *tc
		return onEvent(StreamEvent{Type: "tool_call", ToolCall: &copy})
	}

	finish := func() (ChatResult, error) {
		result.Text = text.String()
		result.ToolCalls = result.ToolCalls[:0]
		for _, id := range toolOrder {
			tc := toolCalls[id]
			if tc == nil {
				continue
			}
			if strings.TrimSpace(tc.Name) == "" {
				tc.Name = "unknown"
			}
			if strings.TrimSpace(tc.Arguments) == "" {
				tc.Arguments = "{}"
			}
			result.ToolCalls = append(result.ToolCalls, *tc)
			if err := emitTool(tc); err != nil {
				return result, err
			}
		}
		if len(eventCounts) > 0 {
			log.Printf("upstream events thread=%s counts=%v text_chars=%d tool_calls=%d",
				threadID, eventCounts, text.Len(), len(toolOrder))
		}
		return result, nil
	}

	terminal := map[string]bool{
		"turn_completed": true,
		"turn_aborted":   true,
		"turn_ended":     true,
		"stream_ended":   true,
		"chat_complete":  true,
	}
	for {
		select {
		case <-ctx.Done():
			res, _ := finish()
			return res, ctx.Err()
		default:
		}
		readWait := 2 * time.Minute
		if !completeDeadline.IsZero() {
			if remaining := time.Until(completeDeadline); remaining > 0 {
				readWait = remaining
			} else {
				return finish()
			}
		}
		_ = conn.SetReadDeadline(time.Now().Add(readWait))
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			res, emitErr := finish()
			if emitErr != nil {
				return res, emitErr
			}
			if text.Len() > 0 || len(toolOrder) > 0 {
				return res, nil
			}
			return res, fmt.Errorf("upstream WebSocket read: %w", err)
		}

		var jsonStr string
		if msgType == websocket.TextMessage {
			jsonStr = string(data)
		} else {
			if gateway {
				jsonStr, err = gatewayDecode(data)
			} else {
				jsonStr, err = aidpDecode(data)
			}
			if err != nil {
				log.Printf("upstream frame decode: %v", err)
				continue
			}
		}

		var env struct {
			Type      string          `json:"type"`
			Payload   json.RawMessage `json:"payload"`
			MessageID string          `json:"message_id"`
			Message   string          `json:"message"`
			Code      string          `json:"code"`
			Error     string          `json:"error"`
			ErrorCode string          `json:"error_code"`
		}
		if err := json.Unmarshal([]byte(jsonStr), &env); err != nil {
			continue
		}
		eventCounts[env.Type]++
		switch env.Type {
		case "text_delta":
			delta := deltaText(env.Payload)
			if delta == "" {
				continue
			}
			text.WriteString(delta)
			if onEvent != nil {
				if err := onEvent(StreamEvent{Type: "text", Text: delta}); err != nil {
					res, _ := finish()
					return res, err
				}
			}
		case "text_complete":
			if text.Len() == 0 {
				var p struct {
					Text string `json:"text"`
				}
				_ = json.Unmarshal(env.Payload, &p)
				if p.Text != "" {
					text.WriteString(p.Text)
					if onEvent != nil {
						if err := onEvent(StreamEvent{Type: "text", Text: p.Text}); err != nil {
							res, _ := finish()
							return res, err
						}
					}
				}
			}
			sawMessageComplete = true
		case "turn_usage":
			var p struct {
				InputTokens      int `json:"input_tokens"`
				OutputTokens     int `json:"output_tokens"`
				TotalTokens      int `json:"total_tokens"`
				CacheReadTokens  int `json:"cache_read_tokens"`
				CacheWriteTokens int `json:"cache_write_tokens"`
			}
			if err := json.Unmarshal(env.Payload, &p); err == nil {
				result.Usage.add(Usage{
					PromptTokens:     p.InputTokens,
					CompletionTokens: p.OutputTokens,
					TotalTokens:      p.TotalTokens,
					CacheReadTokens:  p.CacheReadTokens,
					CacheWriteTokens: p.CacheWriteTokens,
				})
			}
		case "tool_start", "tool_arguments", "tool_arguments_edited":
			var p struct {
				ToolName   string          `json:"tool_name"`
				ToolID     string          `json:"tool_id"`
				ToolCallID string          `json:"tool_call_id"`
				Arguments  json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(env.Payload, &p)
			id := p.ToolID
			if id == "" {
				id = p.ToolCallID
			}
			tc := getTool(id, p.ToolName)
			if len(p.Arguments) > 0 {
				tc.Arguments = normalizeArguments(p.Arguments)
			}
			if err := emitTool(tc); err != nil {
				res, _ := finish()
				return res, err
			}
		case "tool_call", "tool_call_update":
			var p struct {
				ToolCallID      string          `json:"toolCallId"`
				ToolCallIDSnake string          `json:"tool_call_id"`
				ToolID          string          `json:"tool_id"`
				ToolName        string          `json:"tool_name"`
				Title           string          `json:"title"`
				RawInput        json.RawMessage `json:"rawInput"`
				RawInputSnake   json.RawMessage `json:"raw_input"`
				Arguments       json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(env.Payload, &p)
			id := p.ToolID
			if id == "" {
				id = p.ToolCallID
			}
			if id == "" {
				id = p.ToolCallIDSnake
			}
			name := p.ToolName
			if name == "" {
				name = p.Title
			}
			tc := getTool(id, name)
			args := p.RawInput
			if len(args) == 0 {
				args = p.RawInputSnake
			}
			if len(args) == 0 {
				args = p.Arguments
			}
			if len(args) > 0 {
				tc.Arguments = normalizeArguments(args)
			}
			if err := emitTool(tc); err != nil {
				res, _ := finish()
				return res, err
			}
		case "message_complete":
			sawMessageComplete = true
			var p struct {
				ContinuationExpected bool `json:"continuation_expected"`
				ToolCalls            []struct {
					ID         string          `json:"id"`
					ToolCallID string          `json:"tool_call_id"`
					Name       string          `json:"name"`
					ToolName   string          `json:"tool_name"`
					Arguments  json.RawMessage `json:"arguments"`
					Function   struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			}
			_ = json.Unmarshal(env.Payload, &p)
			for _, item := range p.ToolCalls {
				id := item.ID
				if id == "" {
					id = item.ToolCallID
				}
				name := item.Name
				if name == "" {
					name = item.ToolName
				}
				if name == "" {
					name = item.Function.Name
				}
				tc := getTool(id, name)
				args := item.Arguments
				if len(args) == 0 {
					args = item.Function.Arguments
				}
				if len(args) > 0 {
					tc.Arguments = normalizeArguments(args)
				}
				if err := emitTool(tc); err != nil {
					res, _ := finish()
					return res, err
				}
			}
			if p.ContinuationExpected {
				completeDeadline = time.Time{}
			} else {
				completeDeadline = time.Now().Add(5 * time.Second)
			}
		case "error", "session_error", "stream_error", "turn_failed":
			res, _ := finish()
			message := env.Message
			if message == "" {
				message = env.Error
			}
			code := env.Code
			if code == "" {
				code = env.ErrorCode
			}
			log.Printf("upstream %s error=%q code=%q payload=%s frame=%s",
				env.Type, message, code,
				truncateText(string(env.Payload), 1000), truncateText(jsonStr, 1500))
			return res, fmt.Errorf("upstream %s: %s", env.Type, errorText(env.Payload, message, code))
		default:
			if terminal[env.Type] {
				return finish()
			}
			if env.Type == "ping" {
				// Best effort pong; ignore errors.
				_ = conn.WriteControl(websocket.PongMessage, []byte("pong"), time.Now().Add(3*time.Second))
			}
			if sawMessageComplete && env.Type == "session_list" {
				continue
			}
		}
	}
}

func deltaText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var p struct {
		Delta string `json:"delta"`
		Text  string `json:"text"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return ""
	}
	if p.Delta != "" {
		return p.Delta
	}
	return p.Text
}

func errorText(raw json.RawMessage, fallback, code string) string {
	if len(raw) > 0 {
		var v any
		if json.Unmarshal(raw, &v) == nil {
			if s := nestedErrorText(v); s != "" {
				return s
			}
			if compact, err := json.Marshal(v); err == nil && len(compact) > 0 {
				return truncateText(string(compact), 800)
			}
		}
		if s := strings.TrimSpace(string(raw)); s != "" {
			return truncateText(s, 800)
		}
	}
	for _, s := range []string{fallback, code} {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return "unknown upstream error"
}

func nestedErrorText(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case map[string]any:
		for _, key := range []string{"message", "error", "detail", "reason", "description", "title"} {
			if s, ok := x[key]; ok {
				if text := nestedErrorText(s); text != "" {
					return text
				}
			}
		}
	case []any:
		for _, item := range x {
			if text := nestedErrorText(item); text != "" {
				return text
			}
		}
	}
	return ""
}

func truncateText(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// fitUserMessage keeps the user_message payload under the upstream limit.
// Upstream checks the reassembled user_message JSON, so WebSocket
// fragmentation or multiple user_message frames cannot carry one oversized
// turn: extra frames would become separate turns. Trim within the single frame
// instead, keeping the beginning and the most recent tail.
func fitUserMessage(payload map[string]any) error {
	wrap := func() ([]byte, error) {
		return json.Marshal(map[string]any{"type": "user_message", "payload": payload})
	}
	body, err := wrap()
	if err != nil {
		return err
	}
	if len(body) <= maxUserMessageBytes {
		return nil
	}
	original := len(body)
	text, _ := payload["text"].(string)
	if text == "" {
		return fmt.Errorf("%w: system_prompt and metadata alone are %d bytes", ErrUserMessageTooLarge, len(body))
	}
	const marker = "\n\n[earlier conversation trimmed to fit the upstream 64 KB frame limit]\n\n"
	bodyText := text
	for range 5 {
		over := len(body) - maxUserMessageBytes
		runes := []rune(bodyText)
		keep := len(runes) - over - len(marker) - 512
		if keep < 0 {
			keep = 0
		}
		head := keep / 4
		if head > len(runes) {
			head = len(runes)
		}
		rest := keep - head
		if rest > len(runes)-head {
			rest = len(runes) - head
		}
		bodyText = string(runes[:head]) + marker + string(runes[len(runes)-rest:])
		payload["text"] = bodyText
		body, err = wrap()
		if err != nil {
			return err
		}
		if len(body) <= maxUserMessageBytes {
			log.Printf("user_message trimmed %d -> %d bytes (limit %d)", original, len(body), maxUserMessageBytes)
			return nil
		}
	}
	return fmt.Errorf("%w: %d bytes after trimming prompt", ErrUserMessageTooLarge, len(body))
}

func titleFor(prompt string) string {
	clean := strings.Join(strings.Fields(prompt), " ")
	if len(clean) > 60 {
		clean = clean[:60] + "…"
	}
	if clean == "" {
		clean = "New Conversation"
	}
	return clean
}
