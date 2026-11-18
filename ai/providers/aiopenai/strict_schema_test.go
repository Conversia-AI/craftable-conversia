package aiopenai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Conversia-AI/craftable-conversia/ai/llm"
	"github.com/openai/openai-go/v3/option"
)

var strictTestSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"message": map[string]any{"type": "string"},
	},
	"required":             []string{"message"},
	"additionalProperties": false,
}

// captureBody levanta un servidor que guarda el body crudo del request y
// responde con el payload indicado.
func captureBody(t *testing.T, response string) (*httptest.Server, chan map[string]any) {
	t.Helper()
	ch := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("body no es JSON: %v", err)
		}
		ch <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, response)
	}))
	return srv, ch
}

const chatOKResponse = `{"id":"chatcmpl-test","object":"chat.completion","created":0,"model":"gpt-6-luna",` +
	`"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"{\"message\":\"hola\"}"}}],` +
	`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

func chatJSONSchema(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	rf, ok := body["response_format"].(map[string]any)
	if !ok || rf["type"] != "json_schema" {
		t.Fatalf("response_format = %v; want json_schema", body["response_format"])
	}
	js, ok := rf["json_schema"].(map[string]any)
	if !ok {
		t.Fatalf("json_schema = %v", rf["json_schema"])
	}
	return js
}

func TestChatStrictJSONSchemaSendsStrictTrue(t *testing.T) {
	t.Parallel()
	srv, ch := captureBody(t, chatOKResponse)
	defer srv.Close()

	provider := NewOpenAIProvider("test-key", option.WithBaseURL(srv.URL+"/"), option.WithMaxRetries(0))
	if _, err := provider.Chat(context.Background(), []llm.Message{llm.NewUserMessage("hola")},
		llm.WithModel("gpt-6-luna"),
		llm.WithStrictJSONSchemaResponseFormat(strictTestSchema),
	); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	js := chatJSONSchema(t, <-ch)
	if js["strict"] != true {
		t.Errorf("strict = %v; want true", js["strict"])
	}
	if js["name"] != "schema" || js["schema"] == nil {
		t.Errorf("json_schema = %v", js)
	}
}

// Los call sites existentes (WithJSONSchemaResponseFormat) no deben cambiar:
// sin strict en el request.
func TestChatJSONSchemaWithoutStrictOmitsField(t *testing.T) {
	t.Parallel()
	srv, ch := captureBody(t, chatOKResponse)
	defer srv.Close()

	provider := NewOpenAIProvider("test-key", option.WithBaseURL(srv.URL+"/"), option.WithMaxRetries(0))
	if _, err := provider.Chat(context.Background(), []llm.Message{llm.NewUserMessage("hola")},
		llm.WithModel("gpt-6-luna"),
		llm.WithJSONSchemaResponseFormat(strictTestSchema),
	); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	js := chatJSONSchema(t, <-ch)
	if _, present := js["strict"]; present {
		t.Errorf("strict presente sin pedirlo: %v", js)
	}
}

func TestResponsesTextFormatStrict(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		format *llm.ResponseFormat
		strict bool
	}{
		{"strict", &llm.ResponseFormat{Type: llm.JSONSchema, JSONSchema: strictTestSchema, Strict: true}, true},
		{"sin strict", &llm.ResponseFormat{Type: llm.JSONSchema, JSONSchema: strictTestSchema}, false},
	} {
		got, ok := convertToResponsesTextFormat(tc.format)
		if !ok || got.OfJSONSchema == nil {
			t.Fatalf("%s: formato no convertido", tc.name)
		}
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		strict, present := body["strict"]
		if tc.strict && strict != true {
			t.Errorf("%s: strict = %v; want true (%s)", tc.name, strict, raw)
		}
		if !tc.strict && present {
			t.Errorf("%s: strict presente sin pedirlo (%s)", tc.name, raw)
		}
	}
}
