package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

type Server struct {
	cfg      *Config
	sessions *SessionSource
	tokens   *TokenStore
	upstream *Upstream
}

func NewServer(cfg *Config) *Server {
	return &Server{
		cfg:      cfg,
		sessions: NewSessionSource(cfg),
		tokens:   NewTokenStore(cfg.TokensFile),
		upstream: NewUpstream(cfg),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/chat/completions", s.handleChat)
	return s.withCORS(mux)
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Api-Key, X-Thread-Id")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "quickwork-openai"})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	if !s.tokens.Allowed(r) {
		writeOpenAIError(w, http.StatusUnauthorized, "invalid_api_key", "invalid proxy API key")
		return
	}
	created := time.Now().Unix()
	data := make([]map[string]any, 0, len(s.cfg.Models))
	for _, id := range s.cfg.Models {
		data = append(data, map[string]any{
			"id":       id,
			"object":   "model",
			"created":  created,
			"owned_by": "quickwork",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	if !s.tokens.Allowed(r) {
		writeOpenAIError(w, http.StatusUnauthorized, "invalid_api_key", "invalid proxy API key")
		return
	}

	var req ChatRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	if err := dec.Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON body: "+err.Error())
		return
	}
	if req.N > 1 {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "n > 1 is not supported")
		return
	}

	prompt, err := formatPrompt(req.Messages)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	customPrompt := systemPrompt(req.Messages)
	model := s.mapModel(req.Model)
	allowedTools := requestedToolNames(&req)
	choiceMode, choiceName := parseToolChoice(req.ToolChoice)
	if choiceMode == "function" && choiceName != "" {
		allowedTools[choiceName] = true
	}
	parallelTools := true
	if req.ParallelToolCalls != nil {
		parallelTools = *req.ParallelToolCalls
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Timeout)
	defer cancel()

	sess, err := s.sessions.Get(ctx)
	if err != nil {
		s.sessions.invalidate()
		writeOpenAIError(w, http.StatusUnauthorized, "upstream_session_unavailable",
			"no usable QuickWork session: "+err.Error()+"; run the Python login helper")
		return
	}

	threadID := strings.TrimSpace(r.Header.Get("X-Thread-Id"))
	id := "chatcmpl-" + strings.ReplaceAll(uuid(), "-", "")
	created := time.Now().Unix()
	in := ChatInput{
		Prompt:       prompt,
		Model:        model,
		ThreadID:     threadID,
		Title:        titleFor(lastUserText(req.Messages)),
		CustomPrompt: customPrompt,
	}

	if !req.Stream {
		res, err := s.upstream.Chat(ctx, sess, in, nil)
		if err != nil {
			writeOpenAIError(w, http.StatusBadGateway, "upstream_error", err.Error())
			return
		}
		toolCalls := filterToolCalls(res.ToolCalls, allowedTools, choiceMode, choiceName, parallelTools)
		if strings.TrimSpace(res.Text) == "" && len(toolCalls) == 0 {
			writeOpenAIError(w, http.StatusBadGateway, "upstream_error", "upstream returned no text or tool calls")
			return
		}
		message := map[string]any{"role": "assistant"}
		if strings.TrimSpace(res.Text) == "" {
			message["content"] = nil
		} else {
			message["content"] = res.Text
		}
		if len(toolCalls) > 0 {
			message["tool_calls"] = toolCalls
		}
		finish := "stop"
		if strings.TrimSpace(res.Text) == "" && len(toolCalls) > 0 {
			finish = "tool_calls"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      id,
			"object":  "chat.completion",
			"created": created,
			"model":   model,
			"choices": []map[string]any{{
				"index":         0,
				"message":       message,
				"finish_reason": finish,
			}},
			"usage": usageMap(res.Usage),
		})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "streaming_unavailable", "response writer does not support flushing")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if threadID != "" {
		w.Header().Set("X-Thread-Id", threadID)
	}
	w.WriteHeader(http.StatusOK)

	writePayload := func(payload map[string]any) {
		body, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", body)
		flusher.Flush()
	}
	writeChunk := func(delta map[string]any, finish any) {
		writePayload(map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{{
				"index":         0,
				"delta":         delta,
				"finish_reason": finish,
			}},
		})
	}
	writeStreamError := func(message string) {
		writePayload(map[string]any{"error": map[string]any{
			"message": message,
			"type":    "upstream_error",
			"code":    "upstream_error",
		}})
	}

	writeChunk(map[string]any{"role": "assistant"}, nil)

	sawText := false
	sawTool := false
	toolIndex := 0
	toolIndexes := make(map[string]int)
	sentArgs := make(map[string]string)
	emitTool := func(tc ToolCallState) {
		if !toolAllowed(tc, allowedTools, choiceMode, choiceName) {
			return
		}
		idx, seen := toolIndexes[tc.ID]
		if !seen {
			if !parallelTools && sawTool {
				return
			}
			idx = toolIndex
			toolIndex++
			toolIndexes[tc.ID] = idx
			args := strings.TrimSpace(tc.Arguments)
			if args == "" {
				args = "{}"
			}
			sentArgs[tc.ID] = args
			writeChunk(map[string]any{"tool_calls": []map[string]any{{
				"index": idx,
				"id":    tc.ID,
				"type":  "function",
				"function": map[string]any{
					"name":      tc.Name,
					"arguments": args,
				},
			}}}, nil)
			sawTool = true
			return
		}
		args := strings.TrimSpace(tc.Arguments)
		if args == "" {
			args = "{}"
		}
		prev := sentArgs[tc.ID]
		if args == prev {
			return
		}
		delta := args
		if strings.HasPrefix(args, prev) {
			delta = args[len(prev):]
		}
		sentArgs[tc.ID] = args
		if delta == "" {
			return
		}
		writeChunk(map[string]any{"tool_calls": []map[string]any{{
			"index":    idx,
			"function": map[string]any{"arguments": delta},
		}}}, nil)
		sawTool = true
	}

	res, err := s.upstream.Chat(ctx, sess, in, func(ev StreamEvent) error {
		switch ev.Type {
		case "text":
			if ev.Text == "" {
				return nil
			}
			sawText = true
			writeChunk(map[string]any{"content": ev.Text}, nil)
		case "tool_call":
			if ev.ToolCall != nil {
				emitTool(*ev.ToolCall)
			}
		case "tool_call_update":
			if ev.ToolCall != nil {
				emitTool(*ev.ToolCall)
			}
		}
		return nil
	})
	if err != nil {
		writeStreamError(err.Error())
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}
	if !sawText && !sawTool {
		writeStreamError("upstream returned no text or tool calls")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}
	finish := "stop"
	if !sawText && sawTool {
		finish = "tool_calls"
	}
	writeChunk(map[string]any{}, finish)
	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		writePayload(map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []any{},
			"usage":   usageMap(res.Usage),
		})
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func (s *Server) mapModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimPrefix(m, "quickwork/")
	m = strings.TrimPrefix(m, "qw-")
	switch m {
	case "smart", "fast", "balanced", "auto":
		return m
	case "":
		return s.cfg.DefaultModel
	default:
		log.Printf("unknown model %q, using %s", model, s.cfg.DefaultModel)
		return s.cfg.DefaultModel
	}
}

func requestedToolNames(req *ChatRequest) map[string]bool {
	names := make(map[string]bool)
	for _, t := range req.Tools {
		if name := strings.TrimSpace(t.Function.Name); name != "" {
			names[name] = true
		}
	}
	for _, f := range req.Functions {
		if name := strings.TrimSpace(f.Name); name != "" {
			names[name] = true
		}
	}
	return names
}

func parseToolChoice(v any) (mode, name string) {
	switch x := v.(type) {
	case string:
		return strings.ToLower(strings.TrimSpace(x)), ""
	case map[string]any:
		if fn, ok := x["function"].(map[string]any); ok {
			if n, ok := fn["name"].(string); ok {
				return "function", strings.TrimSpace(n)
			}
		}
		if n, ok := x["name"].(string); ok {
			return "function", strings.TrimSpace(n)
		}
		if t, ok := x["type"].(string); ok {
			return strings.ToLower(strings.TrimSpace(t)), ""
		}
	}
	return "auto", ""
}

func toolAllowed(tc ToolCallState, allowed map[string]bool, mode, name string) bool {
	if mode == "none" || len(allowed) == 0 {
		return false
	}
	if !allowed[tc.Name] {
		return false
	}
	if mode == "function" && name != "" && tc.Name != name {
		return false
	}
	return true
}

func filterToolCalls(calls []ToolCallState, allowed map[string]bool, mode, name string, parallel bool) []ToolCall {
	out := make([]ToolCall, 0, len(calls))
	for _, tc := range calls {
		if !toolAllowed(tc, allowed, mode, name) {
			continue
		}
		if !parallel && len(out) > 0 {
			break
		}
		id := strings.TrimSpace(tc.ID)
		if id == "" {
			id = "call_" + strings.ReplaceAll(uuid(), "-", "")
		}
		toolName := strings.TrimSpace(tc.Name)
		if toolName == "" {
			toolName = "unknown"
		}
		args := strings.TrimSpace(tc.Arguments)
		if args == "" {
			args = "{}"
		}
		out = append(out, ToolCall{
			ID:   id,
			Type: "function",
			Function: FunctionCall{
				Name:      toolName,
				Arguments: args,
			},
		})
	}
	return out
}

func usageMap(u Usage) map[string]any {
	completion := u.CompletionTokens
	total := u.TotalTokens
	if total == 0 {
		total = u.PromptTokens + u.CacheReadTokens + u.CacheWriteTokens + completion
	}
	prompt := total - completion
	if prompt < 0 {
		prompt = u.PromptTokens + u.CacheReadTokens + u.CacheWriteTokens
	}
	m := map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": completion,
		"total_tokens":      total,
	}
	if u.CacheReadTokens > 0 {
		m["prompt_tokens_details"] = map[string]any{"cached_tokens": u.CacheReadTokens}
	}
	return m
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeOpenAIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    code,
			"code":    code,
		},
	})
}
