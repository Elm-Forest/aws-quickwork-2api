package proxy

import (
    "bufio"
    "bytes"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync"
    "time"
)

// Anthropic Messages is adapted through the existing OpenAI-compatible engine.
// This preserves account selection, session replay, tool bridging and upstream
// WebSocket behavior in one place.
type anthropicRequest struct {
    Model       string          `json:"model"`
    MaxTokens   int             `json:"max_tokens"`
    System      json.RawMessage `json:"system"`
    Messages    []struct {
        Role    string          `json:"role"`
        Content json.RawMessage `json:"content"`
    } `json:"messages"`
    Stream     bool             `json:"stream"`
    Tools      []struct {
        Name        string `json:"name"`
        Description string `json:"description"`
        InputSchema any    `json:"input_schema"`
    } `json:"tools"`
    ToolChoice struct {
        Type                   string `json:"type"`
        Name                   string `json:"name"`
        DisableParallelToolUse bool   `json:"disable_parallel_tool_use"`
    } `json:"tool_choice"`
}

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        anthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "POST required")
        return
    }
    if !s.tokens.Allowed(r) {
        anthropicError(w, http.StatusUnauthorized, "authentication_error", "invalid proxy API key")
        return
    }
    var incoming anthropicRequest
    if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&incoming); err != nil {
        anthropicError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON request: "+err.Error())
        return
    }
    if strings.TrimSpace(incoming.Model) == "" || len(incoming.Messages) == 0 {
        anthropicError(w, http.StatusBadRequest, "invalid_request_error", "model and messages are required")
        return
    }
    openAI, err := fromAnthropic(incoming)
    if err != nil {
        anthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
        return
    }
    payload, err := json.Marshal(openAI)
    if err != nil {
        anthropicError(w, http.StatusBadRequest, "invalid_request_error", "cannot encode request")
        return
    }
    proxyRequest := r.Clone(r.Context())
    proxyRequest.URL.Path = "/v1/chat/completions"
    proxyRequest.Body = io.NopCloser(bytes.NewReader(payload))
    proxyRequest.ContentLength = int64(len(payload))
    proxyRequest.Header = r.Header.Clone()
    proxyRequest.Header.Set("Content-Type", "application/json")

    if incoming.Stream {
        s.streamAnthropic(w, proxyRequest, incoming.Model)
        return
    }
    captured := httptest.NewRecorder()
    s.handleChat(captured, proxyRequest)
    if captured.Code != http.StatusOK {
        anthropicForwardError(w, captured.Code, captured.Body.Bytes())
        return
    }
    response, err := toAnthropicResponse(captured.Body.Bytes(), incoming.Model)
    if err != nil {
        anthropicError(w, http.StatusBadGateway, "api_error", "invalid internal completion response")
        return
    }
    writeJSON(w, http.StatusOK, response)
}

func anthropicError(w http.ResponseWriter, status int, typ, message string) {
    writeJSON(w, status, map[string]any{
        "type": "error",
        "error": map[string]any{"type": typ, "message": message},
    })
}

func anthropicForwardError(w http.ResponseWriter, status int, original []byte) {
    var upstream struct {
        Error struct {
            Type    string `json:"type"`
            Message string `json:"message"`
        } `json:"error"`
    }
    _ = json.Unmarshal(original, &upstream)
    if upstream.Error.Message == "" {
        upstream.Error.Message = http.StatusText(status)
    }
    kind := "api_error"
    switch status {
    case http.StatusBadRequest, http.StatusMethodNotAllowed:
        kind = "invalid_request_error"
    case http.StatusUnauthorized:
        kind = "authentication_error"
    case http.StatusTooManyRequests:
        kind = "rate_limit_error"
    }
    anthropicError(w, status, kind, upstream.Error.Message)
}

func fromAnthropic(request anthropicRequest) (ChatRequest, error) {
    out := ChatRequest{Model: request.Model, Stream: request.Stream}
    if request.Stream {
        out.StreamOptions = &StreamOptions{IncludeUsage: true}
    }
    if len(request.System) > 0 && string(request.System) != "null" {
        system, err := anthropicText(request.System)
        if err != nil { return out, fmt.Errorf("invalid system: %w", err) }
        if strings.TrimSpace(system) != "" {
            out.Messages = append(out.Messages, Message{Role: "system", Content: system})
        }
    }

    for i, m := range request.Messages {
        role := strings.ToLower(strings.TrimSpace(m.Role))
        if role != "user" && role != "assistant" {
            return out, fmt.Errorf("messages[%d]: role must be user or assistant", i)
        }
        if len(m.Content) == 0 {
            return out, fmt.Errorf("messages[%d]: content missing", i)
        }
        var decoded any
        if err := json.Unmarshal(m.Content, &decoded); err != nil {
            return out, fmt.Errorf("messages[%d]: invalid content: %w", i, err)
        }
        switch value := decoded.(type) {
        case string:
            out.Messages = append(out.Messages, Message{Role: role, Content: value})
        case []any:
            var texts []any
            var toolCalls []ToolCall
            flushUser := func() {
                if len(texts) == 0 { return }
                out.Messages = append(out.Messages, Message{Role: "user", Content: append([]any(nil), texts...)})
                texts = nil
            }
            for j, raw := range value {
                block, ok := raw.(map[string]any)
                if !ok { return out, fmt.Errorf("messages[%d].content[%d]: expected content object", i, j) }
                typ, _ := block["type"].(string)
                switch typ {
                case "text":
                    if text, ok := block["text"].(string); ok {
                        texts = append(texts, map[string]any{"type": "text", "text": text})
                    }
                case "thinking", "redacted_thinking":
                    // Signed thinking traces are opaque and cannot be replayed
                    // as upstream model instructions.
                case "image":
                    source, _ := block["source"].(map[string]any)
                    media, _ := source["media_type"].(string)
                    data, _ := source["data"].(string)
                    if source["type"] == "base64" && strings.HasPrefix(media, "image/") && data != "" {
                        texts = append(texts, map[string]any{
                            "type": "image_url",
                            "image_url": map[string]any{"url": "data:" + media + ";base64," + data},
                        })
                    } else {
                        return out, fmt.Errorf("messages[%d].content[%d]: unsupported image source", i, j)
                    }
                case "tool_use":
                    if role != "assistant" { return out, fmt.Errorf("tool_use requires assistant role") }
                    name, _ := block["name"].(string)
                    toolID, _ := block["id"].(string)
                    if strings.TrimSpace(name) == "" { return out, fmt.Errorf("tool_use.name required") }
                    if toolID == "" { toolID = "call_" + strings.ReplaceAll(uuid(), "-", "") }
                    argument := block["input"]
                    if argument == nil { argument = map[string]any{} }
                    b, err := json.Marshal(argument)
                    if err != nil { return out, err }
                    toolCalls = append(toolCalls, ToolCall{
                        ID: toolID, Type: "function", Function: FunctionCall{Name: name, Arguments: string(b)},
                    })
                case "tool_result":
                    if role != "user" { return out, fmt.Errorf("tool_result requires user role") }
                    flushUser()
                    toolID, _ := block["tool_use_id"].(string)
                    if toolID == "" { return out, fmt.Errorf("tool_result.tool_use_id required") }
                    text := anthropicResultText(block["content"])
                    if block["is_error"] == true { text = "[tool error] " + text }
                    out.Messages = append(out.Messages, Message{
                        Role: "tool", ToolCallID: toolID, Content: text,
                    })
                default:
                    return out, fmt.Errorf("messages[%d].content[%d]: unsupported block type %q", i, j, typ)
                }
            }
            if role == "assistant" {
                out.Messages = append(out.Messages, Message{Role: "assistant", Content: texts, ToolCalls: toolCalls})
            } else {
                flushUser()
            }
        default:
            return out, fmt.Errorf("messages[%d]: content must be text or an array", i)
        }
    }
    if len(out.Messages) == 0 { return out, errors.New("messages cannot be empty") }

    for i, tool := range request.Tools {
        if strings.TrimSpace(tool.Name) == "" {
            return out, fmt.Errorf("tools[%d].name required", i)
        }
        parameters := tool.InputSchema
        if parameters == nil { parameters = map[string]any{"type":"object", "properties":map[string]any{}} }
        out.Tools = append(out.Tools, Tool{
            Type: "function",
            Function: ToolFunction{Name: tool.Name, Description: tool.Description, Parameters: parameters},
        })
    }
    switch request.ToolChoice.Type {
    case "", "auto":
        if len(out.Tools) > 0 { out.ToolChoice = "auto" }
    case "none":
        out.ToolChoice = "none"
    case "any":
        out.ToolChoice = "required"
    case "tool":
        if request.ToolChoice.Name == "" { return out, errors.New("tool_choice.name required") }
        out.ToolChoice = map[string]any{"type":"function", "function":map[string]any{"name":request.ToolChoice.Name}}
    default:
        return out, fmt.Errorf("unsupported tool_choice type %q", request.ToolChoice.Type)
    }
    if request.ToolChoice.DisableParallelToolUse {
        parallel := false
        out.ParallelToolCalls = &parallel
    }
    return out, nil
}

func anthropicText(raw json.RawMessage) (string, error) {
    var value any
    if err := json.Unmarshal(raw, &value); err != nil { return "", err }
    switch x := value.(type) {
    case string:
        return x, nil
    case []any:
        var parts []string
        for _, v := range x {
            block, ok := v.(map[string]any)
            if !ok { return "", errors.New("system blocks must be objects") }
            if block["type"] != "text" { continue }
            if str, ok := block["text"].(string); ok { parts = append(parts, str) }
        }
        return strings.Join(parts, "\n\n"), nil
    default:
        return "", errors.New("system must be text or text blocks")
    }
}

func anthropicResultText(content any) string {
    if content == nil { return "" }
    return contentText(content)
}

func toAnthropicResponse(raw []byte, model string) (map[string]any, error) {
    var resp struct {
        ID string `json:"id"`
        Choices []struct {
            FinishReason string `json:"finish_reason"`
            Message struct {
                Content   json.RawMessage `json:"content"`
                ToolCalls []struct {
                    ID string `json:"id"`
                    Function struct {
                        Name string          `json:"name"`
                        Arguments json.RawMessage `json:"arguments"`
                    } `json:"function"`
                } `json:"tool_calls"`
            } `json:"message"`
        } `json:"choices"`
        Usage struct {
            PromptTokens int `json:"prompt_tokens"`
            CompletionTokens int `json:"completion_tokens"`
        } `json:"usage"`
    }
    if err := json.Unmarshal(raw, &resp); err != nil { return nil, err }
    if len(resp.Choices) == 0 { return nil, errors.New("no choices in completion") }
    choice := resp.Choices[0]
    blocks := make([]map[string]any, 0, len(choice.Message.ToolCalls)+1)
    var text string
    if len(choice.Message.Content) > 0 && string(choice.Message.Content) != "null" {
        if err := json.Unmarshal(choice.Message.Content, &text); err != nil {
            text = string(choice.Message.Content)
        }
    }
    if text != "" {
        blocks = append(blocks, map[string]any{"type":"text", "text":text})
    }
    for _, tool := range choice.Message.ToolCalls {
        input := map[string]any{}
        var argument string
        if err := json.Unmarshal(tool.Function.Arguments, &argument); err != nil {
            argument = string(tool.Function.Arguments)
        }
        if json.Unmarshal([]byte(argument), &input) != nil || input == nil { input = map[string]any{} }
        blocks = append(blocks, map[string]any{
            "type":"tool_use", "id":tool.ID, "name":tool.Function.Name, "input":input,
        })
    }
    stop := "end_turn"
    if len(choice.Message.ToolCalls) > 0 || choice.FinishReason == "tool_calls" {
        stop = "tool_use"
    } else if choice.FinishReason == "length" { stop = "max_tokens" }
    return map[string]any{
        "id":"msg_"+strings.ReplaceAll(uuid(), "-", ""),
        "type":"message", "role":"assistant", "model":model,
        "content":blocks, "stop_reason":stop, "stop_sequence":nil,
        "usage":map[string]any{
            "input_tokens":resp.Usage.PromptTokens,
            "output_tokens":resp.Usage.CompletionTokens,
        },
    }, nil
}

type pipeResponseWriter struct {
    header http.Header
    out *io.PipeWriter
    status chan int
    once sync.Once
}

func (p *pipeResponseWriter) Header() http.Header { return p.header }
func (p *pipeResponseWriter) WriteHeader(code int) {
    p.once.Do(func(){ p.status <- code })
}
func (p *pipeResponseWriter) Write(data []byte) (int,error) {
    p.WriteHeader(http.StatusOK)
    return p.out.Write(data)
}
func (p *pipeResponseWriter) Flush() {}

type anthropicSSE struct {
    out http.ResponseWriter
    flusher http.Flusher
    model string
    started bool
    textBlock int
    blockIndex int
    active int
    stop string
    inputTokens int
    outputTokens int
    failed bool
}

func (s *anthropicSSE) event(kind string, value any) {
    raw,_:=json.Marshal(value)
    _,_ = fmt.Fprintf(s.out, "event: %s\ndata: %s\n\n", kind, raw)
    s.flusher.Flush()
}
func (s *anthropicSSE) start() {
    if s.started { return }
    s.started = true
    s.textBlock = -1
    s.active = -1
    s.stop = "end_turn"
    s.event("message_start",map[string]any{
        "type":"message_start",
        "message":map[string]any{
            "id":"msg_"+strings.ReplaceAll(uuid(),"-",""), "type":"message",
            "role":"assistant", "model":s.model, "content":[]any{},
            "stop_reason":nil, "stop_sequence":nil,
            "usage":map[string]any{"input_tokens":0,"output_tokens":0},
        },
    })
}
func (s *anthropicSSE) closeActive() {
    if s.active >= 0 {
        s.event("content_block_stop",map[string]any{"type":"content_block_stop","index":s.active})
        s.active=-1
    }
}
func (s *anthropicSSE) text(text string) {
    if text=="" { return }
    s.start()
    if s.active < 0 {
        index:=s.blockIndex
        s.blockIndex++
        s.active=index
        s.event("content_block_start",map[string]any{"type":"content_block_start",
            "index":index,"content_block":map[string]any{"type":"text","text":""}})
    }
    s.event("content_block_delta",map[string]any{"type":"content_block_delta",
        "index":s.active,"delta":map[string]any{"type":"text_delta","text":text}})
}
func (s *anthropicSSE) tool(raw map[string]any) {
    s.start()
    s.closeActive()
    index:=s.blockIndex
    s.blockIndex++
    id,_:=raw["id"].(string)
    if id=="" { id="toolu_"+strings.ReplaceAll(uuid(),"-","") }
    fn,_:=raw["function"].(map[string]any)
    name,_:=fn["name"].(string)
    args,_:=fn["arguments"].(string)
    if name=="" { name="unknown" }
    s.event("content_block_start",map[string]any{"type":"content_block_start",
        "index":index,"content_block":map[string]any{"type":"tool_use","id":id,"name":name,"input":map[string]any{}}})
    if args!="" {
        s.event("content_block_delta",map[string]any{"type":"content_block_delta",
            "index":index,"delta":map[string]any{"type":"input_json_delta","partial_json":args}})
    }
    s.event("content_block_stop",map[string]any{"type":"content_block_stop","index":index})
    s.stop="tool_use"
}
func (s *anthropicSSE) complete() {
    if !s.started { s.start() }
    s.closeActive()
    s.event("message_delta",map[string]any{"type":"message_delta",
        "delta":map[string]any{"stop_reason":s.stop,"stop_sequence":nil},
        "usage":map[string]any{"output_tokens":s.outputTokens}})
    s.event("message_stop",map[string]any{"type":"message_stop"})
}

func (s *Server) streamAnthropic(w http.ResponseWriter, proxyRequest *http.Request, model string) {
    flusher,ok:=w.(http.Flusher)
    if !ok {
        anthropicError(w,http.StatusInternalServerError,"api_error","streaming is not supported")
        return
    }
    reader,writer:=io.Pipe()
    bridge:=&pipeResponseWriter{header:make(http.Header),out:writer,status:make(chan int,1)}
    go func(){
        defer writer.Close()
        s.handleChat(bridge,proxyRequest)
        bridge.WriteHeader(http.StatusInternalServerError)
    }()
    var status int
    select {
    case status=<-bridge.status:
    case <-proxyRequest.Context().Done():
        reader.Close()
        return
    }
    defer reader.Close()
    if status!=http.StatusOK {
        body,err:=io.ReadAll(io.LimitReader(reader,1<<20))
        if err!=nil {anthropicError(w,http.StatusBadGateway,"api_error","proxy read failure");return}
        anthropicForwardError(w,status,body)
        return
    }
    renderAnthropicChunks(w, reader, model, flusher)
}

func renderAnthropicChunks(w http.ResponseWriter, reader io.Reader, model string, flusher http.Flusher) {
    w.Header().Set("Content-Type","text/event-stream; charset=utf-8")
    w.Header().Set("Cache-Control","no-cache")
    w.Header().Set("Connection","keep-alive")
    w.Header().Set("X-Accel-Buffering","no")
    w.WriteHeader(http.StatusOK)
    result:=&anthropicSSE{out:w,flusher:flusher,model:model,textBlock:-1,active:-1}
    scanner:=bufio.NewScanner(reader)
    scanner.Buffer(make([]byte,4096),8<<20)
    done:=false
    for scanner.Scan() {
        line:=strings.TrimSpace(scanner.Text())
        if !strings.HasPrefix(line,"data:") {continue}
        body:=strings.TrimSpace(strings.TrimPrefix(line,"data:"))
        if body=="[DONE]" {done=true;break}
        var item map[string]any
        if json.Unmarshal([]byte(body),&item)!=nil {continue}
        if failure,ok:=item["error"].(map[string]any);ok {
            result.start()
            message,_:=failure["message"].(string)
            if message=="" {message="upstream error"}
            result.event("error",map[string]any{"type":"error","error":map[string]any{"type":"api_error","message":message}})
            result.failed=true
            return
        }
        if usage,ok:=item["usage"].(map[string]any);ok {
            if v,ok:=usage["prompt_tokens"].(float64);ok {result.inputTokens=int(v)}
            if v,ok:=usage["completion_tokens"].(float64);ok {result.outputTokens=int(v)}
        }
        choices,ok:=item["choices"].([]any)
        if !ok||len(choices)==0 {continue}
        choice,ok:=choices[0].(map[string]any)
        if !ok {continue}
        delta,_:=choice["delta"].(map[string]any)
        if text,ok:=delta["content"].(string);ok && text!="" {result.text(text)}
        if toolCalls,ok:=delta["tool_calls"].([]any);ok {
            for _,item:=range toolCalls {
                if tool,ok:=item.(map[string]any);ok {result.tool(tool)}
            }
        }
        if reason,ok:=choice["finish_reason"].(string);ok {
            switch reason {
            case "tool_calls":result.stop="tool_use"
            case "length":result.stop="max_tokens"
            }
        }
    }
    if err:=scanner.Err();err!=nil {
        result.start()
        result.event("error",map[string]any{"type":"error","error":map[string]any{"type":"api_error","message":"upstream stream interrupted"}})
        return
    }
    if !done {
        result.start()
        result.event("error",map[string]any{"type":"error","error":map[string]any{"type":"api_error","message":"incomplete upstream stream"}})
        return
    }
    result.complete()
}

func (s *Server) writeAnthropicModels(w http.ResponseWriter, models []map[string]any) {
    data:=make([]map[string]any,0,len(models))
    for _, item:=range models {
        id,_:=item["id"].(string)
        created,_:=item["created"].(int64)
        if created <= 0 { created = time.Now().Unix() }
        data=append(data,map[string]any{
            "type":"model",
            "id":id,
            "display_name":id,
            "created_at":time.Unix(created,0).UTC().Format(time.RFC3339),
        })
    }
    var first,last any
    if len(data)>0 {
        first=data[0]["id"]
        last=data[len(data)-1]["id"]
    }
    writeJSON(w,http.StatusOK,map[string]any{
        "data":data, "first_id":first,"last_id":last,"has_more":false,
    })
}

func (s *Server) handleAnthropicModel(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        anthropicError(w,http.StatusMethodNotAllowed,"invalid_request_error","GET required")
        return
    }
    if !s.tokens.Allowed(r) {
        anthropicError(w,http.StatusUnauthorized,"authentication_error","invalid proxy API key")
        return
    }
    id:=strings.TrimPrefix(r.URL.Path,"/v1/models/")
    for _, model:=range s.models.listForOpenAI(s.cfg.Models) {
        if model["id"] != id { continue }
        created,_:=model["created"].(int64)
        if created<=0 {created=time.Now().Unix()}
        writeJSON(w,http.StatusOK,map[string]any{
            "id":id,"type":"model","display_name":id,
            "created_at":time.Unix(created,0).UTC().Format(time.RFC3339),
        })
        return
    }
    anthropicError(w,http.StatusNotFound,"not_found_error","model not found")
}

func (s *Server) handleAnthropicCountTokens(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        anthropicError(w,http.StatusMethodNotAllowed,"invalid_request_error","POST required")
        return
    }
    if !s.tokens.Allowed(r) {
        anthropicError(w,http.StatusUnauthorized,"authentication_error","invalid proxy API key")
        return
    }
    body,err:=io.ReadAll(http.MaxBytesReader(w,r.Body,8<<20))
    if err!=nil {
        anthropicError(w,http.StatusBadRequest,"invalid_request_error","request is too large")
        return
    }
    var request anthropicRequest
    if err:=json.Unmarshal(body,&request);err!=nil {
        anthropicError(w,http.StatusBadRequest,"invalid_request_error","invalid JSON body")
        return
    }
    converted,err:=fromAnthropic(request)
    if err!=nil {anthropicError(w,http.StatusBadRequest,"invalid_request_error",err.Error());return}
    encoded,_:=json.Marshal(converted)
    // Quick does not expose its model tokenizer. This is only a byte-based
    // approximation to support clients requiring a preflight token estimate.
    count:=(len(encoded)+3)/4
    if count<1 {count=1}
    writeJSON(w,http.StatusOK,map[string]any{"input_tokens":count,"estimated":true})
}
