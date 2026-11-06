package aiopenai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Conversia-AI/craftable-conversia/ai/llm"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
)

// fakeResponsesServer sirve /responses con respuestas guionadas y graba el
// path y el body de cada request.
type fakeResponsesServer struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	paths   []string
	bodies  []string
	replies []string
	status  int
}

func newFakeResponsesServer(t *testing.T, replies ...string) *fakeResponsesServer {
	t.Helper()
	f := &fakeResponsesServer{t: t, replies: replies, status: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		f.mu.Lock()
		idx := len(f.bodies)
		f.paths = append(f.paths, r.URL.Path)
		f.bodies = append(f.bodies, string(raw))
		status := f.status
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if idx < len(f.replies) {
			_, _ = io.WriteString(w, f.replies[idx])
			return
		}
		_, _ = io.WriteString(w, f.replies[len(f.replies)-1])
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeResponsesServer) provider() *OpenAIResponsesProvider {
	return NewOpenAIResponsesProvider("test-key", option.WithBaseURL(f.srv.URL+"/"), option.WithMaxRetries(0))
}

func (f *fakeResponsesServer) body(i int) map[string]any {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var m map[string]any
	if err := json.Unmarshal([]byte(f.bodies[i]), &m); err != nil {
		f.t.Fatalf("body %d no es JSON: %v", i, err)
	}
	return m
}

func (f *fakeResponsesServer) rawBody(i int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[i]
}

const respFunctionCallWithReasoning = `{"id":"resp_1","object":"response","created_at":0,"model":"gpt-6-luna","status":"completed",
"output":[
 {"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"ENC-ONE"},
 {"id":"fc_1","type":"function_call","status":"completed","call_id":"call_A","name":"lookup_part","arguments":"{\"sku\":\"A-1\"}"}
],
"usage":{"input_tokens":200,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":180},"output_tokens":40,"output_tokens_details":{"reasoning_tokens":25},"total_tokens":240}}`

const respFinalMessage = `{"id":"resp_2","object":"response","created_at":0,"model":"gpt-6-luna","status":"completed",
"output":[
 {"id":"msg_1","type":"message","role":"assistant","status":"completed","phase":"final_answer","content":[{"type":"output_text","text":"Listo.","annotations":[]}]}
],
"usage":{"input_tokens":300,"input_tokens_details":{"cached_tokens":200,"cache_write_tokens":0},"output_tokens":10,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":310}}`

func responsesTestTools() []llm.Tool {
	return []llm.Tool{
		{Type: "function", Function: llm.Function{
			Name: "lookup_part", Description: "Looks up a part",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"sku": map[string]any{"type": "string", "description": "SKU"}},
				"required":   []string{"sku"},
			},
		}},
	}
}

// goldenResponsesRequestBody congela el body que el proveedor envía con las
// opciones típicas del runtime v1.5 (ToLLMOptionsResponsesV15 + WithUser):
// effort low, tool_choice auto, temperature 1 y top_p 0 que NO deben viajar,
// seed que no existe en Responses, user que pasa a prompt_cache_key.
const goldenResponsesRequestBody = `{"max_output_tokens":800,"store":false,"prompt_cache_key":"agent:abc","include":["reasoning.encrypted_content"],"input":[{"content":"Eres un agente.","role":"system"},{"content":"hola","role":"user"},{"arguments":"{\"sku\":\"A-1\"}","call_id":"call_1","name":"lookup_part","type":"function_call"},{"call_id":"call_1","output":"{\"ok\":true}","type":"function_call_output"},{"content":"<estado>","role":"system"},{"content":"gracias","role":"user"}],"model":"gpt-6-luna","reasoning":{"effort":"low"},"tool_choice":"auto","tools":[{"strict":false,"parameters":{"properties":{"sku":{"description":"SKU","type":"string"}},"required":["sku"],"type":"object"},"name":"lookup_part","description":"Looks up a part","type":"function"}]}`

func TestResponsesRequestBodyGolden(t *testing.T) {
	t.Parallel()
	f := newFakeResponsesServer(t, respFinalMessage)
	messages := []llm.Message{
		llm.NewSystemMessage("Eres un agente."),
		llm.NewUserMessage("hola"),
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID: "call_1", Type: "function",
			Function: llm.FunctionCall{Name: "lookup_part", Arguments: `{"sku":"A-1"}`},
		}}},
		llm.NewToolMessage("call_1", `{"ok":true}`),
		llm.NewSystemMessage("<estado>"),
		llm.NewUserMessage("gracias"),
	}
	if _, err := f.provider().Chat(context.Background(), messages,
		llm.WithModel("gpt-6-luna"),
		llm.WithTemperature(1),
		llm.WithTopP(0),
		llm.WithSeed(10000),
		llm.WithMaxCompletionTokens(800),
		llm.WithUser("agent:abc"),
		llm.WithReasoningEffort("low"),
		llm.WithTools(responsesTestTools()),
		llm.WithToolChoice("auto"),
	); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if f.paths[0] != "/responses" {
		t.Fatalf("path = %q, want /responses", f.paths[0])
	}
	if got := f.rawBody(0); got != goldenResponsesRequestBody {
		t.Fatalf("request body changed\n got: %s\nwant: %s", got, goldenResponsesRequestBody)
	}
}

func TestResponsesReplaysReasoningWithinTheLoop(t *testing.T) {
	t.Parallel()
	f := newFakeResponsesServer(t, respFunctionCallWithReasoning, respFinalMessage)
	p := f.provider()
	ctx := context.Background()
	opts := []llm.Option{llm.WithModel("gpt-6-luna"), llm.WithReasoningEffort("medium"), llm.WithTools(responsesTestTools()), llm.WithToolChoice("auto")}

	history := []llm.Message{llm.NewSystemMessage("sys"), llm.NewUserMessage("busca A-1")}
	r1, err := p.Chat(ctx, history, opts...)
	if err != nil {
		t.Fatalf("vuelta 1: %v", err)
	}
	if len(r1.Message.ToolCalls) != 1 || r1.Message.ToolCalls[0].ID != "call_A" || r1.Message.ToolCalls[0].Function.Name != "lookup_part" {
		t.Fatalf("tool calls = %+v", r1.Message.ToolCalls)
	}
	if r1.Message.Role != llm.RoleAssistant {
		t.Fatalf("role = %q", r1.Message.Role)
	}
	// El reasoning NO viaja en la metadata (no se persiste).
	for k, v := range r1.Message.Metadata {
		if s, ok := v.(string); ok && strings.Contains(s, "ENC-ONE") {
			t.Fatalf("encrypted_content filtrado a metadata[%s]", k)
		}
	}

	// Como agentx: la memoria devuelve el assistant con tool_calls (sin
	// metadata útil) y el tool result.
	history = append(history,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: r1.Message.ToolCalls},
		llm.NewToolMessage("call_A", `{"stock":3}`),
	)
	if _, err := p.Chat(ctx, history, append(opts, llm.WithToolChoice("auto"))...); err != nil {
		t.Fatalf("vuelta 2: %v", err)
	}
	in := f.body(1)["input"].([]any)
	types := make([]string, 0, len(in))
	for _, it := range in {
		m := it.(map[string]any)
		if t, ok := m["type"].(string); ok {
			types = append(types, t)
		} else {
			types = append(types, "message:"+m["role"].(string))
		}
	}
	want := "message:system,message:user,reasoning,function_call,function_call_output"
	if got := strings.Join(types, ","); got != want {
		t.Fatalf("input types = %s, want %s", got, want)
	}
	rs := in[2].(map[string]any)
	if rs["id"] != "rs_1" || rs["encrypted_content"] != "ENC-ONE" {
		t.Fatalf("reasoning reenviado = %v", rs)
	}
	if sum, ok := rs["summary"].([]any); !ok || len(sum) != 0 {
		t.Fatalf("summary debe ser [] (no omitido): %v", rs["summary"])
	}
	fc := in[3].(map[string]any)
	if fc["call_id"] != "call_A" {
		t.Fatalf("function_call = %v", fc)
	}
	// Con reasoning reenviado, el function_call lleva su id original (forma Q2).
	if fc["id"] != "fc_1" {
		t.Fatalf("function_call debe llevar id fc_1 junto al reasoning: %v", fc)
	}
}

func TestResponsesReplayStoreIsPerInstance(t *testing.T) {
	t.Parallel()
	f := newFakeResponsesServer(t, respFunctionCallWithReasoning, respFinalMessage)
	ctx := context.Background()
	opts := []llm.Option{llm.WithModel("gpt-6-luna"), llm.WithReasoningEffort("low"), llm.WithTools(responsesTestTools())}
	if _, err := f.provider().Chat(ctx, []llm.Message{llm.NewUserMessage("x")}, opts...); err != nil {
		t.Fatal(err)
	}
	// Otra instancia (otro sub-agente u otro turno) no ve el reasoning.
	history := []llm.Message{
		llm.NewUserMessage("x"),
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call_A", Type: "function", Function: llm.FunctionCall{Name: "lookup_part", Arguments: "{}"}}}},
		llm.NewToolMessage("call_A", "{}"),
	}
	if _, err := f.provider().Chat(ctx, history, opts...); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.rawBody(1), `"reasoning"`) && strings.Contains(f.rawBody(1), "ENC-ONE") {
		t.Fatalf("una instancia nueva reenvió reasoning de otra: %s", f.rawBody(1))
	}
}

func TestResponsesUsageAndMetadata(t *testing.T) {
	t.Parallel()
	f := newFakeResponsesServer(t, respFunctionCallWithReasoning)
	r, err := f.provider().Chat(context.Background(), []llm.Message{llm.NewUserMessage("x")}, llm.WithModel("gpt-6-luna"))
	if err != nil {
		t.Fatal(err)
	}
	u := r.Usage
	if u.PromptTokens != 200 || u.CachedPromptTokens != 0 || u.UncachedPromptTokens != 200 || u.CompletionTokens != 40 || u.TotalTokens != 240 || u.CacheWriteTokens != 180 {
		t.Fatalf("usage = %+v", u)
	}
	md := r.Message.Metadata
	checks := map[string]any{
		"openai_model": "gpt-6-luna", "openai_prompt_tokens": 200, "openai_cached_prompt_tokens": 0,
		"openai_uncached_prompt_tokens": 200, "openai_completion_tokens": 40, "openai_total_tokens": 240,
		"openai_reasoning_output_tokens": 25, MetadataKeyOpenAIAPI: MetadataValueOpenAIAPIResponses,
	}
	for k, want := range checks {
		if md[k] != want {
			t.Fatalf("metadata[%s] = %v, want %v", k, md[k], want)
		}
	}
	if _, ok := md[MetadataKeyOpenAIIncompleteReason]; ok {
		t.Fatal("no debe haber incomplete_reason en una respuesta completed")
	}
	if r.Model != "gpt-6-luna" {
		t.Fatalf("model = %q", r.Model)
	}
}

func TestResponsesUsesOnlyFinalAnswerPhase(t *testing.T) {
	t.Parallel()
	const reply = `{"id":"r","object":"response","created_at":0,"model":"gpt-6-luna","status":"completed","output":[
 {"id":"m1","type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Voy a revisar.","annotations":[]}]},
 {"id":"m2","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Hola, ¿en qué te ayudo?","annotations":[]}]}
],"usage":{"input_tokens":1,"input_tokens_details":{"cached_tokens":0},"output_tokens":1,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":2}}`
	f := newFakeResponsesServer(t, reply)
	r, err := f.provider().Chat(context.Background(), []llm.Message{llm.NewUserMessage("x")}, llm.WithModel("gpt-6-luna"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Message.Content != "Hola, ¿en qué te ayudo?" {
		t.Fatalf("content = %q", r.Message.Content)
	}
}

func TestResponsesWithoutPhaseUsesAllText(t *testing.T) {
	t.Parallel()
	const reply = `{"id":"r","object":"response","created_at":0,"model":"gpt-5.1","status":"completed","output":[
 {"id":"m1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Uno.","annotations":[]}]},
 {"id":"m2","type":"message","role":"assistant","content":[{"type":"output_text","text":"Dos.","annotations":[]}]}
],"usage":{"input_tokens":1,"input_tokens_details":{"cached_tokens":0},"output_tokens":1,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":2}}`
	f := newFakeResponsesServer(t, reply)
	r, err := f.provider().Chat(context.Background(), []llm.Message{llm.NewUserMessage("x")}, llm.WithModel("gpt-5.1"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Message.Content != "Uno.Dos." {
		t.Fatalf("content = %q", r.Message.Content)
	}
}

func TestResponsesOnlyCommentaryWithoutToolsKeepsText(t *testing.T) {
	t.Parallel()
	const reply = `{"id":"r","object":"response","created_at":0,"model":"gpt-6-luna","status":"completed","output":[
 {"id":"m1","type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Texto.","annotations":[]}]}
],"usage":{"input_tokens":1,"input_tokens_details":{"cached_tokens":0},"output_tokens":1,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":2}}`
	f := newFakeResponsesServer(t, reply)
	r, err := f.provider().Chat(context.Background(), []llm.Message{llm.NewUserMessage("x")}, llm.WithModel("gpt-6-luna"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Message.Content != "Texto." {
		t.Fatalf("content = %q", r.Message.Content)
	}
}

func TestResponsesIncompleteIsNotAnError(t *testing.T) {
	t.Parallel()
	const reply = `{"id":"r","object":"response","created_at":0,"model":"gpt-6-luna","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},
"output":[{"id":"rs","type":"reasoning","summary":[],"encrypted_content":"E"}],
"usage":{"input_tokens":72,"input_tokens_details":{"cached_tokens":0},"output_tokens":16,"output_tokens_details":{"reasoning_tokens":16},"total_tokens":88}}`
	f := newFakeResponsesServer(t, reply)
	r, err := f.provider().Chat(context.Background(), []llm.Message{llm.NewUserMessage("x")}, llm.WithModel("gpt-6-luna"), llm.WithMaxCompletionTokens(16))
	if err != nil {
		t.Fatalf("incomplete no debe ser error: %v", err)
	}
	if r.Message.Content != "" || r.Message.Metadata[MetadataKeyOpenAIIncompleteReason] != "max_output_tokens" {
		t.Fatalf("content=%q metadata=%v", r.Message.Content, r.Message.Metadata)
	}
	if r.Usage.CompletionTokens != 16 {
		t.Fatalf("usage = %+v", r.Usage)
	}
}

func TestResponsesFailedStatusIsAnError(t *testing.T) {
	t.Parallel()
	const reply = `{"id":"r","object":"response","created_at":0,"model":"gpt-6-luna","status":"failed","error":{"code":"server_error","message":"boom"},"output":[],
"usage":{"input_tokens":0,"input_tokens_details":{"cached_tokens":0},"output_tokens":0,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":0}}`
	f := newFakeResponsesServer(t, reply)
	if _, err := f.provider().Chat(context.Background(), []llm.Message{llm.NewUserMessage("x")}, llm.WithModel("gpt-6-luna")); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestResponsesHTTPErrorIsAnError(t *testing.T) {
	t.Parallel()
	f := newFakeResponsesServer(t, `{"error":{"message":"Unsupported parameter: 'temperature'","type":"invalid_request_error"}}`)
	f.status = http.StatusBadRequest
	if _, err := f.provider().Chat(context.Background(), []llm.Message{llm.NewUserMessage("x")}, llm.WithModel("gpt-6-luna")); err == nil {
		t.Fatal("se esperaba error con 400")
	}
}

func TestResponsesSamplingOnlyWithoutEffortOrNone(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		effort    string
		wantTemp  bool
		wantTopP  bool
		wantEff   string
		hasEffort bool
	}{
		{"sin effort en modelo que razona", "", false, false, "", false},
		{"none", "none", true, true, "none", true},
		{"low", "low", false, false, "low", true},
		{"medium", "MEDIUM", false, false, "medium", true},
		{"desconocido no se envía (y sin sampling)", "turbo", false, false, "", false},
		{"xhigh", "xhigh", false, false, "xhigh", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeResponsesServer(t, respFinalMessage)
			opts := []llm.Option{llm.WithModel("gpt-6-luna"), llm.WithTemperature(0.2), llm.WithTopP(0.9)}
			if tc.effort != "" {
				opts = append(opts, llm.WithReasoningEffort(tc.effort))
			}
			if _, err := f.provider().Chat(context.Background(), []llm.Message{llm.NewUserMessage("x")}, opts...); err != nil {
				t.Fatal(err)
			}
			b := f.body(0)
			_, hasTemp := b["temperature"]
			_, hasTopP := b["top_p"]
			if hasTemp != tc.wantTemp || hasTopP != tc.wantTopP {
				t.Fatalf("temperature=%v top_p=%v, want %v %v (%s)", hasTemp, hasTopP, tc.wantTemp, tc.wantTopP, f.rawBody(0))
			}
			r, hasR := b["reasoning"].(map[string]any)
			if hasR != tc.hasEffort {
				t.Fatalf("reasoning presente=%v, want %v (%s)", hasR, tc.hasEffort, f.rawBody(0))
			}
			if hasR && r["effort"] != tc.wantEff {
				t.Fatalf("effort = %v, want %s", r["effort"], tc.wantEff)
			}
		})
	}
}

func TestResponsesToolChoiceFunctionRoleAndMaxTokens(t *testing.T) {
	t.Parallel()
	f := newFakeResponsesServer(t, respFinalMessage)
	messages := []llm.Message{
		llm.NewUserMessage("x"),
		{Role: llm.RoleAssistant, Content: "Voy a buscar.", ToolCalls: []llm.ToolCall{{ID: "call_F", Type: "function", Function: llm.FunctionCall{Name: "f", Arguments: "{}"}}}},
		llm.NewFunctionMessage("call_F", "resultado"),
		llm.NewAssistantMessage("Hecho."),
	}
	if _, err := f.provider().Chat(context.Background(), messages,
		llm.WithModel("gpt-6-luna"),
		llm.WithToolChoice(map[string]any{"type": "function"}),
		llm.WithMaxTokens(5),
		llm.WithPromptCacheKey("clave"),
		llm.WithUser("ignorado"),
	); err != nil {
		t.Fatal(err)
	}
	b := f.body(0)
	if b["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %v", b["tool_choice"])
	}
	if b["max_output_tokens"] != float64(16) {
		t.Fatalf("max_output_tokens = %v (piso 16)", b["max_output_tokens"])
	}
	if b["prompt_cache_key"] != "clave" {
		t.Fatalf("prompt_cache_key = %v", b["prompt_cache_key"])
	}
	if _, ok := b["user"]; ok {
		t.Fatal("user no debe enviarse")
	}
	in := b["input"].([]any)
	// user, assistant texto, function_call, function_call_output(call_F), assistant
	if len(in) != 5 {
		t.Fatalf("input = %v", in)
	}
	if in[1].(map[string]any)["content"] != "Voy a buscar." || in[2].(map[string]any)["type"] != "function_call" {
		t.Fatalf("assistant con texto + tool call mal armado: %v", in)
	}
	if out := in[3].(map[string]any); out["type"] != "function_call_output" || out["call_id"] != "call_F" {
		t.Fatalf("function role = %v", out)
	}
}

func TestResponsesUnsupportedRoleAndStream(t *testing.T) {
	t.Parallel()
	f := newFakeResponsesServer(t, respFinalMessage)
	if _, err := f.provider().Chat(context.Background(), []llm.Message{{Role: "raro", Content: "x"}}, llm.WithModel("gpt-6-luna")); err == nil {
		t.Fatal("rol desconocido debe dar error")
	}
	if _, err := f.provider().ChatStream(context.Background(), nil); err != ErrResponsesStreamNotSupported {
		t.Fatalf("ChatStream err = %v", err)
	}
}

func TestResponsesSamplingSinEffortEnModeloQueNoRazona(t *testing.T) {
	t.Parallel()
	f := newFakeResponsesServer(t, respFinalMessage)
	if _, err := f.provider().Chat(context.Background(), []llm.Message{llm.NewUserMessage("x")}, llm.WithModel("gpt-4.1"), llm.WithTemperature(0.1)); err != nil {
		t.Fatal(err)
	}
	if b := f.body(0); b["temperature"] == nil {
		t.Fatalf("gpt-4.1 sin effort debe llevar temperature: %s", f.rawBody(0))
	}
}

func TestResponsesToolSinParametersYAssistantVacio(t *testing.T) {
	t.Parallel()
	f := newFakeResponsesServer(t, respFinalMessage)
	tools := []llm.Tool{{Type: "function", Function: llm.Function{Name: "sin_params", Description: "x"}}}
	messages := []llm.Message{llm.NewUserMessage("x"), llm.NewAssistantMessage(""), llm.NewUserMessage("y")}
	if _, err := f.provider().Chat(context.Background(), messages, llm.WithModel("gpt-6-luna"), llm.WithTools(tools)); err != nil {
		t.Fatal(err)
	}
	b := f.body(0)
	params := b["tools"].([]any)[0].(map[string]any)["parameters"].(map[string]any)
	if params["type"] != "object" {
		t.Fatalf("parameters por defecto = %v", params)
	}
	if in := b["input"].([]any); len(in) != 2 {
		t.Fatalf("el assistant vacío no debe enviarse: %v", in)
	}
}

func TestResponsesDescartaFunctionCallIncompleto(t *testing.T) {
	t.Parallel()
	const reply = `{"id":"r","object":"response","created_at":0,"model":"gpt-6-luna","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[
 {"id":"rs","type":"reasoning","summary":[],"encrypted_content":"E"},
 {"id":"fc_ok","type":"function_call","status":"completed","call_id":"call_ok","name":"f","arguments":"{}"},
 {"id":"fc_cut","type":"function_call","status":"incomplete","call_id":"call_cut","name":"f","arguments":"{\"a\":"}
],"usage":{"input_tokens":1,"input_tokens_details":{"cached_tokens":0},"output_tokens":1,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":2}}`
	f := newFakeResponsesServer(t, reply)
	p := f.provider()
	r, err := p.Chat(context.Background(), []llm.Message{llm.NewUserMessage("x")}, llm.WithModel("gpt-6-luna"))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Message.ToolCalls) != 1 || r.Message.ToolCalls[0].ID != "call_ok" {
		t.Fatalf("tool calls = %+v", r.Message.ToolCalls)
	}
	if _, ids := p.replay.itemsFor([]llm.ToolCall{{ID: "call_cut"}}); ids["call_cut"] != "" {
		t.Fatal("un function_call incompleto no debe guardarse en el store")
	}
}

func TestResponsesReplayStoreEviction(t *testing.T) {
	t.Parallel()
	s := newReasoningReplayStore(2)
	for _, id := range []string{"a", "b", "c"} {
		raw := `{"id":"r","object":"response","created_at":0,"model":"m","status":"completed","output":[` +
			`{"id":"rs_` + id + `","type":"reasoning","summary":[],"encrypted_content":"E` + id + `"},` +
			`{"id":"fc","type":"function_call","call_id":"` + id + `","name":"f","arguments":"{}"}]}`
		var resp responses.Response
		if err := json.Unmarshal([]byte(raw), &resp); err != nil {
			t.Fatal(err)
		}
		s.remember(&resp)
	}
	if got, _ := s.itemsFor([]llm.ToolCall{{ID: "a"}}); got != nil {
		t.Fatalf("a debió desalojarse: %v", got)
	}
	got, ids := s.itemsFor([]llm.ToolCall{{ID: "zz"}, {ID: "c"}})
	if len(got) != 1 || got[0].OfReasoning == nil || got[0].OfReasoning.ID != "rs_c" || ids["c"] != "fc" {
		t.Fatalf("c debió quedar: %+v %v", got, ids)
	}
	// Un reasoning sin encrypted_content no se guarda (la API lo ignoraría).
	var noEnc responses.Response
	_ = json.Unmarshal([]byte(`{"id":"r","object":"response","created_at":0,"model":"m","status":"completed","output":[{"id":"rs_x","type":"reasoning","summary":[]},{"id":"fc","type":"function_call","call_id":"x","name":"f","arguments":"{}"}]}`), &noEnc)
	s.remember(&noEnc)
	if got, _ := s.itemsFor([]llm.ToolCall{{ID: "x"}}); got != nil {
		t.Fatalf("sin encrypted_content no debe guardarse: %v", got)
	}
}
