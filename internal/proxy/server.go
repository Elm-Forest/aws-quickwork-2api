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
	model := s.mapModel(req.Model)

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

	if !req.Stream {
		var text strings.Builder
		_, err := s.upstream.Chat(ctx, sess, prompt, model, threadID, func(delta string) error {
			text.WriteString(delta)
			return nil
		})
		if err != nil {
			writeOpenAIError(w, http.StatusBadGateway, "upstream_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      id,
			"object":  "chat.completion",
			"created": created,
			"model":   model,
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": text.String()},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
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

	writeChunk := func(delta map[string]any, finish any) {
		payload := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{{
				"index":         0,
				"delta":         delta,
				"finish_reason": finish,
			}},
		}
		body, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", body)
		flusher.Flush()
	}

	writeChunk(map[string]any{"role": "assistant"}, nil)
	_, err = s.upstream.Chat(ctx, sess, prompt, model, threadID, func(delta string) error {
		writeChunk(map[string]any{"content": delta}, nil)
		return nil
	})
	if err != nil {
		body, _ := json.Marshal(map[string]any{"error": map[string]any{
			"message": err.Error(),
			"type":    "upstream_error",
			"code":    "upstream_error",
		}})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", body)
		flusher.Flush()
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}
	writeChunk(map[string]any{}, "stop")
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
