package aiclaude

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/Conversia-AI/craftable-conversia/ai/llm"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// ClaudeProvider implements the LLM interface for Anthropic Claude.
type ClaudeProvider struct {
	client anthropic.Client
}

// NewClaudeProvider creates a new Claude provider.
func NewClaudeProvider(apiKey string, opts ...option.RequestOption) *ClaudeProvider {
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}

	options := append([]option.RequestOption{option.WithAPIKey(apiKey)}, opts...)
	client := anthropic.NewClient(options...)

	return &ClaudeProvider{
		client: client,
	}
}

func defaultChatOptions() *llm.ChatOptions {
	options := llm.DefaultOptions()
	options.Model = string(anthropic.ModelClaudeSonnet4_5)
	options.MaxTokens = 2048
	return options
}

// Chat implements the LLM interface.
func (p *ClaudeProvider) Chat(ctx context.Context, messages []llm.Message, opts ...llm.Option) (llm.Response, error) {
	options := defaultChatOptions()
	for _, opt := range opts {
		opt(options)
	}

	anthropicMessages, systemBlocks, err := convertToAnthropicMessages(messages)
	if err != nil {
		return llm.Response{}, err
	}

	params, requestOpts := buildAnthropicParams(options, anthropicMessages, systemBlocks)
	message, err := p.client.Messages.New(ctx, params, requestOpts...)
	if err != nil {
		return llm.Response{}, err
	}

	return convertFromAnthropicResponse(message)
}

// ChatStream implements streaming for the Anthropic Messages API.
func (p *ClaudeProvider) ChatStream(ctx context.Context, messages []llm.Message, opts ...llm.Option) (llm.Stream, error) {
	options := defaultChatOptions()
	for _, opt := range opts {
		opt(options)
	}

	anthropicMessages, systemBlocks, err := convertToAnthropicMessages(messages)
	if err != nil {
		return nil, err
	}

	params, requestOpts := buildAnthropicParams(options, anthropicMessages, systemBlocks)
	stream := p.client.Messages.NewStreaming(ctx, params, requestOpts...)

	return &anthropicStream{
		stream: stream,
	}, nil
}

type anthropicStream struct {
	stream interface {
		Next() bool
		Current() anthropic.MessageStreamEventUnion
		Err() error
		Close() error
	}
	message   anthropic.Message
	lastError error
}

func (s *anthropicStream) Next() (llm.Message, error) {
	if s.lastError != nil {
		return llm.Message{}, s.lastError
	}

	if !s.stream.Next() {
		if err := s.stream.Err(); err != nil {
			s.lastError = err
			return llm.Message{}, err
		}
		s.lastError = io.EOF
		return llm.Message{}, io.EOF
	}

	event := s.stream.Current()
	if err := s.message.Accumulate(event); err != nil {
		s.lastError = err
		return llm.Message{}, err
	}

	message, err := convertFromAnthropicMessage(&s.message)
	if err != nil {
		s.lastError = err
		return llm.Message{}, err
	}

	return message, nil
}

func (s *anthropicStream) Close() error {
	return s.stream.Close()
}

func buildAnthropicParams(
	options *llm.ChatOptions,
	messages []anthropic.MessageParam,
	system []anthropic.TextBlockParam,
) (anthropic.MessageNewParams, []option.RequestOption) {
	maxTokens := options.MaxTokens
	if options.MaxCompletionTokens > 0 {
		maxTokens = options.MaxCompletionTokens
	}
	if maxTokens <= 0 {
		maxTokens = 2048
	}

	params := anthropic.MessageNewParams{
		MaxTokens: int64(maxTokens),
		Messages:  messages,
		Model:     anthropic.Model(options.Model),
	}

	if len(system) > 0 {
		params.System = system
	}

	// Anthropic does not allow temperature and top_p simultaneously.
	// Models using reasoning/effort (e.g. claude-opus-4-7) reject temperature.
	if options.ReasoningEffort == "" {
		if options.Temperature != 0 {
			params.Temperature = anthropic.Float(float64(options.Temperature))
		} else {
			params.Temperature = anthropic.Float(0)
		}
	}

	if len(options.Stop) > 0 {
		params.StopSequences = options.Stop
	}

	if options.User != "" {
		params.Metadata = anthropic.MetadataParam{
			UserID: anthropic.String(options.User),
		}
	}

	outputConfig, hasOutputConfig := convertToAnthropicOutputConfig(options, options.Model)
	if hasOutputConfig {
		params.OutputConfig = outputConfig
	}

	if len(options.Tools) > 0 || len(options.Functions) > 0 {
		params.Tools = convertToAnthropicTools(options.Tools, options.Functions)
	}

	if len(params.Tools) > 0 && options.ToolChoice != nil {
		if toolChoice, ok := convertToAnthropicToolChoice(options.ToolChoice); ok {
			params.ToolChoice = toolChoice
		}
	}

	requestOpts := make([]option.RequestOption, 0, len(options.Headers))
	for key, value := range options.Headers {
		requestOpts = append(requestOpts, option.WithHeader(key, value))
	}

	return params, requestOpts
}

func convertToAnthropicMessages(messages []llm.Message) ([]anthropic.MessageParam, []anthropic.TextBlockParam, error) {
	anthropicMessages := make([]anthropic.MessageParam, 0, len(messages))
	systemBlocks := make([]anthropic.TextBlockParam, 0)

	for _, msg := range messages {
		if msg.Role == llm.RoleSystem {
			systemBlocks = append(systemBlocks, anthropic.TextBlockParam{Text: msg.Content})
			continue
		}

		anthropicMessage, err := convertToAnthropicMessage(msg)
		if err != nil {
			return nil, nil, err
		}

		anthropicMessages = append(anthropicMessages, anthropicMessage)
	}

	return anthropicMessages, systemBlocks, nil
}

func convertToAnthropicMessage(msg llm.Message) (anthropic.MessageParam, error) {
	switch msg.Role {
	case llm.RoleUser:
		return anthropic.NewUserMessage(anthropic.NewTextBlock(msg.Content)), nil
	case llm.RoleAssistant:
		blocks := make([]anthropic.ContentBlockParamUnion, 0, 1+len(msg.ToolCalls))
		if msg.Content != "" || len(msg.ToolCalls) == 0 {
			blocks = append(blocks, anthropic.NewTextBlock(msg.Content))
		}

		for _, toolCall := range msg.ToolCalls {
			input, err := parseToolArguments(toolCall.Function.Arguments)
			if err != nil {
				return anthropic.MessageParam{}, err
			}

			blocks = append(blocks, anthropic.NewToolUseBlock(toolCall.ID, input, toolCall.Function.Name))
		}

		return anthropic.NewAssistantMessage(blocks...), nil
	case llm.RoleFunction, llm.RoleTool:
		toolCallID := msg.ToolCallID
		if toolCallID == "" {
			toolCallID = msg.Name
		}
		if toolCallID == "" {
			return anthropic.MessageParam{}, errors.New("tool result message requires tool_call_id or name")
		}

		return anthropic.NewUserMessage(anthropic.NewToolResultBlock(toolCallID, msg.Content, false)), nil
	default:
		return anthropic.MessageParam{}, errors.New("unsupported role: " + msg.Role)
	}
}

func parseToolArguments(arguments string) (any, error) {
	if strings.TrimSpace(arguments) == "" {
		return map[string]any{}, nil
	}

	var input any
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return nil, err
	}

	return input, nil
}

func convertToAnthropicTools(tools []llm.Tool, functions []llm.Function) []anthropic.ToolUnionParam {
	result := make([]anthropic.ToolUnionParam, 0, len(tools)+len(functions))

	for _, tool := range tools {
		if tool.Type != "function" {
			continue
		}

		result = append(result, convertFunctionToAnthropicTool(tool.Function))
	}

	for _, fn := range functions {
		result = append(result, convertFunctionToAnthropicTool(fn))
	}

	return result
}

func convertFunctionToAnthropicTool(fn llm.Function) anthropic.ToolUnionParam {
	tool := anthropic.ToolParam{
		Name:        fn.Name,
		InputSchema: convertToAnthropicToolSchema(fn.Parameters),
	}

	if fn.Description != "" {
		tool.Description = anthropic.String(fn.Description)
	}

	return anthropic.ToolUnionParam{
		OfTool: &tool,
	}
}

func convertToAnthropicToolSchema(schema any) anthropic.ToolInputSchemaParam {
	schemaMap := schemaToMap(schema)
	if len(schemaMap) == 0 {
		return anthropic.ToolInputSchemaParam{}
	}

	result := anthropic.ToolInputSchemaParam{}

	if properties, ok := schemaMap["properties"]; ok {
		result.Properties = properties
	}

	if required, ok := schemaMap["required"]; ok {
		result.Required = toStringSlice(required)
	}

	extras := make(map[string]any)
	for key, value := range schemaMap {
		switch key {
		case "type", "properties", "required":
		default:
			extras[key] = value
		}
	}

	if len(extras) > 0 {
		result.ExtraFields = extras
	}

	return result
}

func convertToAnthropicToolChoice(toolChoice any) (anthropic.ToolChoiceUnionParam, bool) {
	switch choice := toolChoice.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(choice)) {
		case "", "auto":
			return anthropic.ToolChoiceUnionParam{
				OfAuto: &anthropic.ToolChoiceAutoParam{},
			}, true
		case "required":
			return anthropic.ToolChoiceUnionParam{
				OfAny: &anthropic.ToolChoiceAnyParam{},
			}, true
		case "none":
			none := anthropic.NewToolChoiceNoneParam()
			return anthropic.ToolChoiceUnionParam{
				OfNone: &none,
			}, true
		default:
			return anthropic.ToolChoiceParamOfTool(choice), true
		}
	case map[string]any:
		if name := toolChoiceName(choice); name != "" {
			return anthropic.ToolChoiceParamOfTool(name), true
		}
	}

	return anthropic.ToolChoiceUnionParam{}, false
}

func toolChoiceName(choice map[string]any) string {
	if name, ok := choice["name"].(string); ok {
		return name
	}

	functionValue, ok := choice["function"]
	if !ok {
		return ""
	}

	functionMap, ok := functionValue.(map[string]any)
	if !ok {
		return ""
	}

	name, _ := functionMap["name"].(string)
	return name
}

func convertToAnthropicOutputConfig(options *llm.ChatOptions, model string) (anthropic.OutputConfigParam, bool) {
	var config anthropic.OutputConfigParam
	hasConfig := false

	if options.JSONMode {
		config.Format = anthropic.JSONOutputFormatParam{
			Schema: genericJSONObjectSchema(),
		}
		hasConfig = true
	} else if options.ResponseFormat != nil {
		switch options.ResponseFormat.Type {
		case llm.JSONObject:
			config.Format = anthropic.JSONOutputFormatParam{
				Schema: genericJSONObjectSchema(),
			}
			hasConfig = true
		case llm.JSONSchema:
			// Anthropic's output_config.format grammar compiler cannot handle
			// schemas with more than 24 optional parameters or very large grammars.
			// Complex schemas (like bulk analysis) exceed both limits regardless of
			// how required/optional fields are distributed. The model produces valid
			// JSON without enforcement because the prompt instructs the format; the
			// caller uses extractJSONFromContent to parse the text response.
			// No config.Format is set here intentionally.
		}
	}

	if anthropicModelSupportsEffort(model) {
		if effort, ok := convertToAnthropicReasoningEffort(options.ReasoningEffort); ok {
			config.Effort = effort
			hasConfig = true
		}
	}

	return config, hasConfig
}

// anthropicModelSupportsEffort reports whether a model supports the effort parameter.
// Validated against the Anthropic API — models not listed here return 400 if effort is sent.
func anthropicModelSupportsEffort(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "claude-opus-4-7",
		"claude-sonnet-4-6",
		"claude-opus-4-6",
		"claude-opus-4-5":
		return true
	default:
		return false
	}
}

func convertToAnthropicReasoningEffort(effort string) (anthropic.OutputConfigEffort, bool) {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "":
		return "", false
	case "minimal", "low":
		return anthropic.OutputConfigEffortLow, true
	case "medium":
		return anthropic.OutputConfigEffortMedium, true
	case "high":
		return anthropic.OutputConfigEffortHigh, true
	case "xhigh":
		return anthropic.OutputConfigEffortXhigh, true
	case "max":
		return anthropic.OutputConfigEffortMax, true
	default:
		return anthropic.OutputConfigEffortMedium, true
	}
}

func convertFromAnthropicResponse(message *anthropic.Message) (llm.Response, error) {
	if message == nil {
		return llm.Response{}, errors.New("nil anthropic response")
	}

	llmMessage, err := convertFromAnthropicMessage(message)
	if err != nil {
		return llm.Response{}, err
	}

	return llm.Response{
		Model:   string(message.Model),
		Message: llmMessage,
		Usage:   convertAnthropicUsage(message.Usage),
	}, nil
}

func convertFromAnthropicMessage(message *anthropic.Message) (llm.Message, error) {
	if message == nil {
		return llm.Message{}, errors.New("nil anthropic message")
	}

	result := llm.Message{
		Role: llm.RoleAssistant,
	}

	var content strings.Builder
	toolCalls := make([]llm.ToolCall, 0)

	for _, block := range message.Content {
		switch variant := block.AsAny().(type) {
		case anthropic.TextBlock:
			content.WriteString(variant.Text)
		case anthropic.ToolUseBlock:
			arguments := string(variant.Input)
			if arguments == "" {
				arguments = "{}"
			}

			toolCalls = append(toolCalls, llm.ToolCall{
				ID:   variant.ID,
				Type: "function",
				Function: llm.FunctionCall{
					Name:      variant.Name,
					Arguments: arguments,
				},
			})
		}
	}

	result.Content = content.String()
	if len(toolCalls) > 0 {
		result.ToolCalls = toolCalls
	}

	if message.Model != "" || message.StopReason != "" || message.Usage.InputTokens > 0 || message.Usage.OutputTokens > 0 {
		result.Metadata = map[string]any{
			"anthropic_model":                       string(message.Model),
			"anthropic_stop_reason":                 string(message.StopReason),
			"anthropic_input_tokens":                int(message.Usage.InputTokens),
			"anthropic_cache_creation_input_tokens": int(message.Usage.CacheCreationInputTokens),
			"anthropic_cache_read_input_tokens":     int(message.Usage.CacheReadInputTokens),
			"anthropic_output_tokens":               int(message.Usage.OutputTokens),
			"anthropic_total_tokens":                convertAnthropicUsage(message.Usage).TotalTokens,
		}
	}

	return result, nil
}

func convertAnthropicUsage(usage anthropic.Usage) llm.Usage {
	promptTokens := int(usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens)
	cachedPromptTokens := int(usage.CacheReadInputTokens)
	uncachedPromptTokens := int(usage.InputTokens + usage.CacheCreationInputTokens)
	completionTokens := int(usage.OutputTokens)

	return llm.Usage{
		PromptTokens:         promptTokens,
		CachedPromptTokens:   cachedPromptTokens,
		UncachedPromptTokens: uncachedPromptTokens,
		CompletionTokens:     completionTokens,
		TotalTokens:          promptTokens + completionTokens,
	}
}

func schemaToMap(schema any) map[string]any {
	if schema == nil {
		return nil
	}

	if schemaMap, ok := schema.(map[string]any); ok {
		return schemaMap
	}

	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return nil
	}

	var schemaMap map[string]any
	if err := json.Unmarshal(schemaBytes, &schemaMap); err != nil {
		return nil
	}

	return schemaMap
}

func toStringSlice(value any) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		result := make([]string, 0, len(v))
		for _, item := range v {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func genericJSONObjectSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
	}
}

// enforceAdditionalProperties recursively walks a JSON schema and sets
// "additionalProperties": false on every node with "type": "object".
// Required by the Anthropic API for structured output schemas.
func enforceAdditionalProperties(schema map[string]any) map[string]any {
	result := make(map[string]any, len(schema))
	for k, v := range schema {
		result[k] = v
	}

	if result["type"] == "object" {
		result["additionalProperties"] = false
	}

	if props, ok := result["properties"].(map[string]any); ok {
		newProps := make(map[string]any, len(props))
		for k, v := range props {
			if nested, ok := v.(map[string]any); ok {
				newProps[k] = enforceAdditionalProperties(nested)
			} else {
				newProps[k] = v
			}
		}
		result["properties"] = newProps
	}

	if items, ok := result["items"].(map[string]any); ok {
		result["items"] = enforceAdditionalProperties(items)
	}

	for _, key := range []string{"anyOf", "allOf", "oneOf"} {
		if arr, ok := result[key].([]any); ok {
			newArr := make([]any, len(arr))
			for i, elem := range arr {
				if nested, ok := elem.(map[string]any); ok {
					newArr[i] = enforceAdditionalProperties(nested)
				} else {
					newArr[i] = elem
				}
			}
			result[key] = newArr
		}
	}

	return result
}
