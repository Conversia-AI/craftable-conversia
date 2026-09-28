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
	"github.com/openai/openai-go/v3/shared"
)

func TestConvertToOpenAIReasoningEffort(t *testing.T) {
	t.Parallel()

	cases := map[string]shared.ReasoningEffort{
		"none":    "none",
		"NONE":    "none",
		"low":     shared.ReasoningEffortLow,
		"Low":     shared.ReasoningEffortLow,
		"medium":  shared.ReasoningEffortMedium,
		"high":    shared.ReasoningEffortHigh,
		"minimal": shared.ReasoningEffortMinimal,
		// Unrecognized values keep falling back to medium.
		"xhigh": shared.ReasoningEffortMedium,
		"max":   shared.ReasoningEffortMedium,
		"bogus": shared.ReasoningEffortMedium,
		"":      shared.ReasoningEffortMedium,
	}

	for input, want := range cases {
		if got := convertToOpenAIReasoningEffort(input); got != want {
			t.Errorf("convertToOpenAIReasoningEffort(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestChatSendsReasoningEffortOnTheWire checks the request body that reaches
// /v1/chat/completions, not just the converted option.
func TestChatSendsReasoningEffortOnTheWire(t *testing.T) {
	t.Parallel()

	tools := []llm.Tool{{
		Type: "function",
		Function: llm.Function{
			Name:        "lookup_part",
			Description: "Looks up a part",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}}

	cases := []struct {
		name       string
		effort     string
		wantEffort string // "" means the field must be absent
	}{
		{name: "none with tools", effort: "none", wantEffort: "none"},
		{name: "low with tools", effort: "low", wantEffort: "low"},
		{name: "unrecognized falls back to medium", effort: "xhigh", wantEffort: "medium"},
		{name: "omitted", effort: "", wantEffort: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			body := captureChatRequest(t,
				llm.WithModel("gpt-5.6-luna"),
				llm.WithTools(tools),
				llm.WithToolChoice("auto"),
				llm.WithReasoningEffort(tc.effort),
			)

			got, present := body["reasoning_effort"]
			if tc.wantEffort == "" {
				if present {
					t.Fatalf("reasoning_effort = %v, want the field absent", got)
				}
				return
			}
			if got != tc.wantEffort {
				t.Fatalf("reasoning_effort = %v, want %q", got, tc.wantEffort)
			}
			if _, ok := body["tools"]; !ok {
				t.Fatalf("tools missing from request body: %v", body)
			}
		})
	}
}

func captureChatRequest(t *testing.T, opts ...llm.Option) map[string]any {
	t.Helper()

	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-test","object":"chat.completion","created":0,"model":"gpt-5.6-luna",`+
			`"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],`+
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	provider := NewOpenAIProvider("test-key", option.WithBaseURL(srv.URL+"/"), option.WithMaxRetries(0))
	if _, err := provider.Chat(context.Background(), []llm.Message{llm.NewUserMessage("hola")}, opts...); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if body == nil {
		t.Fatal("no request reached the test server")
	}
	return body
}
