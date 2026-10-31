package aiopenai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Conversia-AI/craftable-conversia/ai/llm"
	"github.com/openai/openai-go/v3/option"
)

// goldenV1013ChatRequestBody es el body crudo que v1.0.13 envía a
// /chat/completions con las opciones que usa el runtime v1.4 (sin
// WithPromptCacheKey). Se compara como string exacto: cualquier cambio de valor,
// de campo o de orden de serialización en el request de v1.4 rompe este test.
const goldenV1013ChatRequestBody = `{"messages":[{"content":"Eres un agente.","role":"system"},{"content":"hola","role":"user"},{"content":"","tool_calls":[{"id":"call_1","function":{"arguments":"{\"sku\":\"A-1\"}","name":"lookup_part"},"type":"function"}],"role":"assistant"},{"content":"{\"ok\":true}","tool_call_id":"call_1","role":"tool"},{"content":"gracias","role":"user"}],"model":"gpt-5.6-luna","max_tokens":800,"temperature":0.20000000298023224,"top_p":1,"user":"session-1","reasoning_effort":"none","tool_choice":"auto","tools":[{"function":{"name":"lookup_part","description":"Looks up a part","parameters":{"properties":{"sku":{"description":"SKU","type":"string"}},"required":["sku"],"type":"object"}},"type":"function"},{"function":{"name":"transfer","description":"Transfers the chat","parameters":{"properties":{},"type":"object"}},"type":"function"}]}`

func TestChatRequestBodyGoldenWithoutNewOptions(t *testing.T) {
	t.Parallel()

	rawCh := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		rawCh <- string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-test","object":"chat.completion","created":0,"model":"gpt-5.6-luna",`+
			`"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],`+
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	tools := []llm.Tool{
		{Type: "function", Function: llm.Function{
			Name: "lookup_part", Description: "Looks up a part",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"sku": map[string]any{"type": "string", "description": "SKU"}},
				"required":   []string{"sku"},
			},
		}},
		{Type: "function", Function: llm.Function{
			Name: "transfer", Description: "Transfers the chat",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		}},
	}
	messages := []llm.Message{
		llm.NewSystemMessage("Eres un agente."),
		llm.NewUserMessage("hola"),
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID: "call_1", Type: "function",
			Function: llm.FunctionCall{Name: "lookup_part", Arguments: `{"sku":"A-1"}`},
		}}},
		llm.NewToolMessage("call_1", `{"ok":true}`),
		llm.NewUserMessage("gracias"),
	}

	provider := NewOpenAIProvider("test-key", option.WithBaseURL(srv.URL+"/"), option.WithMaxRetries(0))
	if _, err := provider.Chat(context.Background(), messages,
		llm.WithModel("gpt-5.6-luna"),
		llm.WithMaxTokens(800),
		llm.WithUser("session-1"),
		llm.WithTemperature(0.2),
		llm.WithReasoningEffort("none"),
		llm.WithTools(tools),
		llm.WithToolChoice("auto"),
	); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	got := <-rawCh
	if got != goldenV1013ChatRequestBody {
		t.Fatalf("request body changed\n got: %s\nwant: %s", got, goldenV1013ChatRequestBody)
	}
}
