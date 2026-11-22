package proxy

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "quickwork-openai/internal/config"
)

func TestAnthropicRequestConversion(t *testing.T) {
    raw := `{
        "model":"claude-opus-5-5",
        "max_tokens":512,
        "system":[{"type":"text","text":"Keep answers short."}],
        "messages":[
            {"role":"user","content":"Hi"},
            {"role":"assistant","content":[
                {"type":"text","text":"Calling."},
                {"type":"tool_use","id":"toolu_1","name":"lookup","input":{"query":"hello"}}
            ]},
            {"role":"user","content":[
                {"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"result"}]},
                {"type":"text","text":"Continue"}
            ]}
        ],
        "tools":[{"name":"lookup","description":"Search","input_schema":{"type":"object","properties":{"query":{"type":"string"}}}}],
        "tool_choice":{"type":"tool","name":"lookup","disable_parallel_tool_use":true}
    }`
    var req anthropicRequest
    if err:=json.Unmarshal([]byte(raw),&req);err!=nil {t.Fatal(err)}
    converted,err:=fromAnthropic(req)
    if err!=nil {t.Fatal(err)}
    if got:=len(converted.Messages);got!=5 {t.Fatalf("want system+user+assistant+tool+user, got %d: %#v",got,converted.Messages)}
    if converted.Messages[0].Role!="system" || !strings.Contains(contentText(converted.Messages[0].Content),"Keep answers short") {
        t.Fatalf("system lost: %#v",converted.Messages[0])
    }
    if len(converted.Messages[2].ToolCalls)!=1 || converted.Messages[2].ToolCalls[0].Function.Name!="lookup" {
        t.Fatalf("assistant tool call lost: %#v",converted.Messages[2])
    }
    if converted.Messages[3].Role!="tool" || converted.Messages[3].ToolCallID!="toolu_1" ||
        !strings.Contains(contentText(converted.Messages[3].Content),"result") {
        t.Fatalf("tool result lost: %#v",converted.Messages[3])
    }
    if len(converted.Tools)!=1 || converted.ParallelToolCalls==nil || *converted.ParallelToolCalls {
        t.Fatalf("tool properties lost: %+v",converted)
    }
    prompt,err:=formatPrompt(converted.Messages)
    if err!=nil || !strings.Contains(prompt,"Tool result") { t.Fatalf("prompt incorrect: %q %v",prompt,err) }
}

func TestAnthropicResponseConversion(t *testing.T) {
    raw := `{"id":"chatcmpl-test","choices":[{"finish_reason":"tool_calls","message":{
      "role":"assistant","content":"Working",
      "tool_calls":[{"id":"call-1","type":"function","function":{"name":"search","arguments":"{\"q\":\"x\"}"}}]
    }}],"usage":{"prompt_tokens":13,"completion_tokens":7}}`
    result,err:=toAnthropicResponse([]byte(raw),"claude-sonnet-4-5")
    if err!=nil {t.Fatal(err)}
    if result["stop_reason"]!="tool_use" || result["model"]!="claude-sonnet-4-5" {
        t.Fatalf("wrong stop or model: %#v",result)
    }
    blocks:=result["content"].([]map[string]any)
    if len(blocks)!=2 || blocks[0]["type"]!="text" || blocks[1]["type"]!="tool_use" { t.Fatalf("wrong content: %#v",blocks)}
    input:=blocks[1]["input"].(map[string]any)
    if input["q"]!="x" {t.Fatalf("incorrect tool arguments: %#v",input)}
    usage:=result["usage"].(map[string]any)
    if usage["input_tokens"]!=13 || usage["output_tokens"]!=7 {t.Fatalf("usage incorrect: %#v",usage)}
}

func TestAnthropicStreamTranslation(t *testing.T) {
    raw := strings.Join([]string{
        `data: {"id":"c1","choices":[{"delta":{"role":"assistant"},"finish_reason":null}]}`,
        `data: {"id":"c1","choices":[{"delta":{"content":"hello "},"finish_reason":null}]}`,
        `data: {"id":"c1","choices":[{"delta":{"content":"world"},"finish_reason":null}]}`,
        `data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"toolu_1","function":{"name":"lookup","arguments":"{\"q\":1}"}}]},"finish_reason":null}]}`,
        `data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
        `data: {"id":"c1","choices":[],"usage":{"prompt_tokens":44,"completion_tokens":12}}`,
        "data: [DONE]",
    },"\n\n")+"\n\n"
    rec:=httptest.NewRecorder()
    renderAnthropicChunks(rec,strings.NewReader(raw),"claude-opus-5-5",rec)
    if rec.Code!=http.StatusOK {t.Fatalf("status: %d",rec.Code)}
    body:=rec.Body.String()
    expected:=[]string{
        "event: message_start","event: content_block_start","event: content_block_delta",
        "event: content_block_stop","event: content_block_start",
        `"type":"input_json_delta"`,
        "event: message_delta",`"stop_reason":"tool_use"`,"event: message_stop",
    }
    current:=0
    for _,piece:=range expected {
        offset:=strings.Index(body[current:],piece)
        if offset<0 {t.Fatalf("missing or out-of-order %q in %s",piece,body)}
        current+=offset+len(piece)
    }
}

func TestAnthropicEndpointValidationAndModels(t *testing.T) {
    cfg:=config.Default()
    cfg.APIKeys=[]string{"sk-test"}
    srv:=NewServer(&cfg,nil)
    handler:=srv.Handler()
    request:=func(method,path,body string,headers map[string]string)*httptest.ResponseRecorder{
        t.Helper()
        req:=httptest.NewRequest(method,path,strings.NewReader(body))
        for k,v:=range headers {req.Header.Set(k,v)}
        rec:=httptest.NewRecorder()
        handler.ServeHTTP(rec,req)
        return rec
    }
    rec:=request("POST","/v1/messages",`{"model":"smart","messages":[{"role":"user","content":"hello"}]}`,nil)
    if rec.Code!=401 || !strings.Contains(rec.Body.String(),"authentication_error") {
        t.Fatalf("wrong unauthorized response: %d %s",rec.Code,rec.Body.String())
    }
    headers:=map[string]string{"x-api-key":"sk-test","anthropic-version":"2023-06-01"}
    rec=request("POST","/v1/messages",`{"model":"smart","messages":[]}`,headers)
    if rec.Code!=400 || !strings.Contains(rec.Body.String(),"invalid_request_error") {
        t.Fatalf("wrong validation response: %d %s",rec.Code,rec.Body.String())
    }
    for _,path:=range []string{"/v1/models","/v1/models/smart"} {
        rec=request("GET",path,"",headers)
        if rec.Code!=200 {t.Fatalf("%s: %d %s",path,rec.Code,rec.Body.String())}
    }
    rec=request("GET","/v1/models","",map[string]string{"Authorization":"Bearer sk-test"})
    if rec.Code!=200 || !strings.Contains(rec.Body.String(),`"object":"list"`) {
        t.Fatalf("OpenAI models changed: %d %s",rec.Code,rec.Body.String())
    }
    rec=request("POST","/v1/messages/count_tokens",`{"model":"smart","messages":[{"role":"user","content":"hello"}]}`,headers)
    if rec.Code!=200 || !strings.Contains(rec.Body.String(),`"estimated":true`) {
        t.Fatalf("count tokens response: %d %s",rec.Code,rec.Body.String())
    }
    for model,want:=range map[string]string{
        "claude-haiku-5-5":"fast",
        "claude-sonnet-4-5":"balanced",
        "claude-opus-5-5":"smart",
        "claude-3-5-haiku-20241022":"fast",
        "claude-3-7-sonnet-latest":"balanced",
    } {
        if actual:=srv.mapModel(model);actual!=want {t.Errorf("routing %s: got %s want %s",model,actual,want)}
    }
}

func TestAnthropicMalformedMessageBlocks(t *testing.T) {
    for _,block:=range []string{
       `{"type":"document","source":{"type":"url","url":"http://example.com/doc.pdf"}}`,
       `{"type":"tool_result","tool_use_id":""}`,
    } {
        var req anthropicRequest
        raw:=`{"model":"smart","messages":[{"role":"user","content":[`+block+`]}]}`
        if err:=json.Unmarshal([]byte(raw),&req);err!=nil {t.Fatal(err)}
        if _,err:=fromAnthropic(req);err==nil {t.Fatalf("expected unsupported message error for %s",block)}
    }
}
