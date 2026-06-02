package aiclaude

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Conversia-AI/craftable-conversia/ai/llm"
	"github.com/anthropics/anthropic-sdk-go"
)

func TestConvertToAnthropicMessages(t *testing.T) {
	t.Parallel()

	messages := []llm.Message{
		llm.NewSystemMessage("You are the system."),
		llm.NewUserMessage("Hello Claude"),
		{
			Role:    llm.RoleAssistant,
			Content: "Calling tool",
			ToolCalls: []llm.ToolCall{
				{
					ID:   "tool_1",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "lookup_weather",
						Arguments: `{"city":"Lima"}`,
					},
				},
			},
		},
		llm.NewToolMessage("tool_1", `{"temperature":24}`),
	}

	anthropicMessages, systemBlocks, err := convertToAnthropicMessages(messages)
	if err != nil {
		t.Fatalf("convertToAnthropicMessages() error = %v", err)
	}

	if len(systemBlocks) != 1 || systemBlocks[0].Text != "You are the system." {
		t.Fatalf("unexpected system blocks: %#v", systemBlocks)
	}

	if len(anthropicMessages) != 3 {
		t.Fatalf("expected 3 anthropic messages, got %d", len(anthropicMessages))
	}

	userJSON, err := json.Marshal(anthropicMessages[0])
	if err != nil {
		t.Fatalf("marshal user message: %v", err)
	}
	assertJSONEqual(t, string(userJSON), `{"content":[{"text":"Hello Claude","type":"text"}],"role":"user"}`)

	assistantJSON, err := json.Marshal(anthropicMessages[1])
	if err != nil {
		t.Fatalf("marshal assistant message: %v", err)
	}
	assertJSONEqual(t, string(assistantJSON), `{"content":[{"text":"Calling tool","type":"text"},{"id":"tool_1","input":{"city":"Lima"},"name":"lookup_weather","type":"tool_use"}],"role":"assistant"}`)

	toolJSON, err := json.Marshal(anthropicMessages[2])
	if err != nil {
		t.Fatalf("marshal tool message: %v", err)
	}
	assertJSONEqual(t, string(toolJSON), `{"content":[{"content":[{"text":"{\"temperature\":24}","type":"text"}],"is_error":false,"tool_use_id":"tool_1","type":"tool_result"}],"role":"user"}`)
}

func TestConvertFromAnthropicResponse(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"id":"msg_1",
		"content":[
			{"type":"text","text":"Here is the answer. "},
			{"type":"tool_use","id":"tool_42","name":"lookup_customer","input":{"email":"ana@example.com"},"caller":{"type":"direct"}}
		],
		"model":"claude-sonnet-4-5",
		"role":"assistant",
		"stop_details":{},
		"stop_reason":"tool_use",
		"stop_sequence":"",
		"type":"message",
		"usage":{
			"cache_creation":{},
			"cache_creation_input_tokens":5,
			"cache_read_input_tokens":11,
			"inference_geo":"us",
			"input_tokens":30,
			"output_tokens":9,
			"server_tool_use":{},
			"service_tier":"standard"
		}
	}`)

	var message anthropic.Message
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	response, err := convertFromAnthropicResponse(&message)
	if err != nil {
		t.Fatalf("convertFromAnthropicResponse() error = %v", err)
	}

	if response.Model != "claude-sonnet-4-5" {
		t.Fatalf("unexpected model: %s", response.Model)
	}

	if response.Message.Content != "Here is the answer. " {
		t.Fatalf("unexpected content: %q", response.Message.Content)
	}

	if len(response.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(response.Message.ToolCalls))
	}

	toolCall := response.Message.ToolCalls[0]
	if toolCall.ID != "tool_42" || toolCall.Function.Name != "lookup_customer" || toolCall.Function.Arguments != `{"email":"ana@example.com"}` {
		t.Fatalf("unexpected tool call: %#v", toolCall)
	}

	if response.Usage.PromptTokens != 46 {
		t.Fatalf("unexpected prompt tokens: %d", response.Usage.PromptTokens)
	}

	if response.Usage.CachedPromptTokens != 11 {
		t.Fatalf("unexpected cached prompt tokens: %d", response.Usage.CachedPromptTokens)
	}

	if response.Usage.UncachedPromptTokens != 35 {
		t.Fatalf("unexpected uncached prompt tokens: %d", response.Usage.UncachedPromptTokens)
	}

	if response.Usage.CompletionTokens != 9 || response.Usage.TotalTokens != 55 {
		t.Fatalf("unexpected usage: %#v", response.Usage)
	}
}

func TestBuildAnthropicParamsSendsExplicitZeroTemperature(t *testing.T) {
	t.Parallel()

	options := defaultChatOptions()
	llm.WithTemperature(0)(options)

	params, _ := buildAnthropicParams(options, []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock("hello")),
	}, nil)

	if !params.Temperature.Valid() {
		t.Fatal("temperature should be sent when explicitly configured as zero")
	}
	if params.Temperature.Value != 0 {
		t.Fatalf("temperature = %v, want 0", params.Temperature.Value)
	}
	if params.TopP.Valid() {
		t.Fatalf("top_p should not be sent with temperature, got %v", params.TopP.Value)
	}
}

func TestBuildAnthropicParamsSkipsTemperatureForReasoning(t *testing.T) {
	t.Parallel()

	options := defaultChatOptions()
	llm.WithTemperature(0)(options)
	llm.WithReasoningEffort("low")(options)

	params, _ := buildAnthropicParams(options, []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock("hello")),
	}, nil)

	if params.Temperature.Valid() {
		t.Fatalf("temperature should not be sent for reasoning models, got %v", params.Temperature.Value)
	}
}

func assertJSONEqual(t *testing.T, actual string, expected string) {
	t.Helper()

	var actualValue any
	if err := json.Unmarshal([]byte(actual), &actualValue); err != nil {
		t.Fatalf("unmarshal actual json: %v", err)
	}

	var expectedValue any
	if err := json.Unmarshal([]byte(expected), &expectedValue); err != nil {
		t.Fatalf("unmarshal expected json: %v", err)
	}

	if !reflect.DeepEqual(actualValue, expectedValue) {
		t.Fatalf("unexpected json.\nactual: %s\nexpected: %s", actual, expected)
	}
}
