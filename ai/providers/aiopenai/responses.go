package aiopenai

// Proveedor de OpenAI sobre la Responses API (POST /v1/responses).
//
// Es un llm.LLM aparte de OpenAIProvider: el camino de chat/completions
// (OpenAIProvider.Chat) no cambia ni un byte. Lo usa el runtime v1.5 de
// core-ai-v3 detrás de su gate (épica agent-v1.5-responses-api, 03_diseno.md
// §2), para correr modelos con reasoning effort + function tools, cosa que
// gpt-5.4+/gpt-6 solo aceptan por esta API.
//
// Decisiones (03_diseno.md §2 y 02_responses_api.md):
//   - Sin estado en OpenAI: store=false en cada request; la conversación
//     completa viaja en input, como en chat.
//   - Reasoning solo dentro del turno: los ítems reasoning (cifrados) de una
//     respuesta con tool calls se guardan en un store EN MEMORIA de esta
//     instancia, indexados por call_id, y se reenvían antes del function_call
//     correspondiente en la vuelta siguiente del loop de tools. Nunca van a
//     llm.Message.Metadata, así que no se persisten en ninguna memoria. Una
//     instancia por sub-agente y turno = un store por sub-agente y turno.
//   - Sin temperature/top_p cuando hay effort distinto de "none" (la API los
//     rechaza con 400 en gpt-6-luna/gpt-6.1-terra). Sin seed, stop,
//     penalties ni logit_bias (no existen en Responses).
//   - tools con strict=false explícito (Responses asume strict si se omite).
//   - user (deprecado en Responses) se usa como prompt_cache_key si no se
//     pasó una clave explícita, para conservar el ruteo de caché de chat.
//   - Salida: solo el texto de los mensajes con phase "final_answer" (si la
//     API manda phase); role siempre "assistant".

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"

	"github.com/Conversia-AI/craftable-conversia/ai/llm"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// ErrResponsesStreamNotSupported lo devuelve ChatStream: el proveedor de
// Responses no implementa streaming.
var ErrResponsesStreamNotSupported = errors.New("aiopenai: streaming is not supported by the Responses provider")

// responsesReplayMaxEntries acota el store de reasoning de una instancia.
const responsesReplayMaxEntries = 256

// responsesMinOutputTokens es el mínimo que acepta max_output_tokens.
const responsesMinOutputTokens = 16

// Claves de metadata propias del camino Responses (además de las openai_*
// que también llena el camino de chat).
const (
	MetadataKeyOpenAIAPI              = "openai_api"
	MetadataValueOpenAIAPIResponses   = "responses"
	MetadataKeyOpenAIIncompleteReason = "openai_incomplete_reason"
)

// OpenAIResponsesProvider implementa llm.LLM sobre la Responses API.
type OpenAIResponsesProvider struct {
	client openai.Client
	replay *reasoningReplayStore
}

// NewOpenAIResponsesProvider crea el proveedor. Con apiKey vacía usa
// OPENAI_API_KEY (igual que NewOpenAIProvider). Cada instancia tiene su
// propio store de reasoning: crear una por sub-agente y turno.
func NewOpenAIResponsesProvider(apiKey string, opts ...option.RequestOption) *OpenAIResponsesProvider {
	inner := NewOpenAIProvider(apiKey, opts...)
	return &OpenAIResponsesProvider{
		client: inner.client,
		replay: newReasoningReplayStore(responsesReplayMaxEntries),
	}
}

// Chat implementa llm.LLM.
func (p *OpenAIResponsesProvider) Chat(ctx context.Context, messages []llm.Message, opts ...llm.Option) (llm.Response, error) {
	options := defaultChatOptions()
	for _, opt := range opts {
		opt(options)
	}

	params, reqOpts, err := p.buildResponsesParams(messages, options)
	if err != nil {
		return llm.Response{}, err
	}

	resp, err := p.client.Responses.New(ctx, params, reqOpts...)
	if err != nil {
		return llm.Response{}, err
	}
	switch resp.Status {
	case responses.ResponseStatusCompleted, responses.ResponseStatusIncomplete, "":
	default:
		// failed, cancelled, queued, in_progress: sin background no deberían
		// llegar; si llegan, no hay una respuesta usable (CR1-03).
		msg := strings.TrimSpace(resp.Error.Message)
		if msg == "" {
			msg = "response status " + string(resp.Status)
		}
		return llm.Response{}, errors.New("aiopenai responses: " + msg)
	}

	out := convertFromResponsesResponse(resp)
	p.replay.remember(resp)
	return out, nil
}

// ChatStream no está soportado en el proveedor de Responses.
func (p *OpenAIResponsesProvider) ChatStream(ctx context.Context, messages []llm.Message, opts ...llm.Option) (llm.Stream, error) {
	return nil, ErrResponsesStreamNotSupported
}

func (p *OpenAIResponsesProvider) buildResponsesParams(messages []llm.Message, options *llm.ChatOptions) (responses.ResponseNewParams, []option.RequestOption, error) {
	input, err := p.convertToResponsesInput(messages)
	if err != nil {
		return responses.ResponseNewParams{}, nil, err
	}

	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(options.Model),
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: input},
		Store: openai.Bool(false),
		Include: []responses.ResponseIncludable{
			responses.ResponseIncludableReasoningEncryptedContent,
		},
	}

	effort, effortSet := normalizeResponsesEffort(options.ReasoningEffort)
	if effortSet {
		params.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffort(effort)}
	}

	// temperature/top_p: solo con effort "none", o sin effort en un modelo que
	// no razona. Con effort (o sin effort en gpt-5*/gpt-6*/o*, que por default
	// razonan) la API los rechaza con 400 (02 §1.2; revisión RV2-05).
	samplingAllowed := effort == "none" || (!effortSet && !isReasoningModelFamily(options.Model))
	if samplingAllowed && options.Temperature != 0 {
		params.Temperature = openai.Float(float64(options.Temperature))
	}
	if samplingAllowed && options.TopP != 0 {
		params.TopP = openai.Float(float64(options.TopP))
	}

	maxTokens := options.MaxCompletionTokens
	if maxTokens <= 0 {
		maxTokens = options.MaxTokens
	}
	if maxTokens > 0 {
		if maxTokens < responsesMinOutputTokens {
			maxTokens = responsesMinOutputTokens
		}
		params.MaxOutputTokens = openai.Int(int64(maxTokens))
	}

	cacheKey := strings.TrimSpace(options.PromptCacheKey)
	if cacheKey == "" {
		cacheKey = strings.TrimSpace(options.User)
	}
	if cacheKey != "" {
		params.PromptCacheKey = openai.String(cacheKey)
	}

	if len(options.Tools) > 0 || len(options.Functions) > 0 {
		if tools := convertToResponsesTools(options.Tools, options.Functions); len(tools) > 0 {
			params.Tools = tools
		}
	}

	if options.ToolChoice != nil {
		params.ToolChoice = convertToResponsesToolChoice(options.ToolChoice)
	}

	if options.JSONMode {
		params.Text = responses.ResponseTextConfigParam{
			Format: responses.ResponseFormatTextConfigUnionParam{OfJSONObject: &shared.ResponseFormatJSONObjectParam{}},
		}
	} else if options.ResponseFormat != nil {
		if format, ok := convertToResponsesTextFormat(options.ResponseFormat); ok {
			params.Text = responses.ResponseTextConfigParam{Format: format}
		}
	}

	var reqOpts []option.RequestOption
	for k, v := range options.Headers {
		reqOpts = append(reqOpts, option.WithHeader(k, v))
	}

	return params, reqOpts, nil
}

// isReasoningModelFamily dice si el modelo razona por default (y por lo tanto
// rechaza temperature/top_p sin effort "none").
func isReasoningModelFamily(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	// Las variantes "-chat" (gpt-5-chat-latest, gpt-5.1-chat-latest) no
	// razonan y aceptan temperature (CR1-04).
	if strings.Contains(m, "-chat") {
		return false
	}
	for _, p := range []string{"gpt-5", "gpt-6", "o1", "o3", "o4"} {
		if strings.HasPrefix(m, p) {
			return true
		}
	}
	return false
}

// normalizeResponsesEffort devuelve el effort a enviar. A diferencia del
// camino de chat (que manda "medium" ante un valor desconocido), un valor
// vacío o desconocido NO se envía: la API usa el default del modelo.
func normalizeResponsesEffort(effort string) (string, bool) {
	switch e := strings.ToLower(strings.TrimSpace(effort)); e {
	case "none", "minimal", "low", "medium", "high", "xhigh":
		return e, true
	default:
		return "", false
	}
}

func (p *OpenAIResponsesProvider) convertToResponsesInput(messages []llm.Message) (responses.ResponseInputParam, error) {
	input := make(responses.ResponseInputParam, 0, len(messages))
	// Un mismo reasoning no se reenvía dos veces en un request (si las tool
	// calls de una respuesta quedaran en varios mensajes; CR1-01).
	reasoningEnviado := map[string]bool{}
	for _, msg := range messages {
		switch msg.Role {
		case llm.RoleSystem:
			input = append(input, responses.ResponseInputItemParamOfMessage(msg.Content, responses.EasyInputMessageRoleSystem))
		case llm.RoleUser:
			input = append(input, responses.ResponseInputItemParamOfMessage(msg.Content, responses.EasyInputMessageRoleUser))
		case llm.RoleAssistant:
			if len(msg.ToolCalls) == 0 {
				// Un assistant vacío (p. ej. una respuesta incomplete que solo
				// trajo reasoning) no aporta nada y no se envía (RV3-04).
				if strings.TrimSpace(msg.Content) == "" {
					continue
				}
				input = append(input, responses.ResponseInputItemParamOfMessage(msg.Content, responses.EasyInputMessageRoleAssistant))
				continue
			}
			// (1) reasoning de este turno que precedió a estas tool calls, con
			// los ids de sus function_call (forma del sondeo Q2; RV2-01).
			reasoning, fcIDs := p.replay.itemsFor(msg.ToolCalls)
			for _, r := range reasoning {
				if r.OfReasoning != nil && reasoningEnviado[r.OfReasoning.ID] {
					continue
				}
				if r.OfReasoning != nil {
					reasoningEnviado[r.OfReasoning.ID] = true
				}
				input = append(input, r)
			}
			// (2) texto que acompañó a las tool calls, si lo hubo.
			if strings.TrimSpace(msg.Content) != "" {
				input = append(input, responses.ResponseInputItemParamOfMessage(msg.Content, responses.EasyInputMessageRoleAssistant))
			}
			// (3) un function_call por tool call. Lleva su id solo si se
			// reenvía el reasoning (el id no se guarda en la memoria).
			for _, tc := range msg.ToolCalls {
				fc := responses.ResponseInputItemParamOfFunctionCall(tc.Function.Arguments, tc.ID, tc.Function.Name)
				if id := fcIDs[tc.ID]; id != "" {
					fc.OfFunctionCall.ID = openai.String(id)
				}
				input = append(input, fc)
			}
		case llm.RoleTool:
			input = append(input, responses.ResponseInputItemParamOfFunctionCallOutput(msg.ToolCallID, msg.Content))
		case llm.RoleFunction:
			// Igual que el camino de chat: Name hace de call id.
			input = append(input, responses.ResponseInputItemParamOfFunctionCallOutput(msg.Name, msg.Content))
		default:
			return nil, errors.New("unsupported role: " + msg.Role)
		}
	}
	return input, nil
}

func convertToResponsesTools(tools []llm.Tool, functions []llm.Function) []responses.ToolUnionParam {
	result := make([]responses.ToolUnionParam, 0, len(tools)+len(functions))
	add := func(fn llm.Function) {
		paramsJSON, _ := json.Marshal(fn.Parameters)
		var parametersMap map[string]any
		_ = json.Unmarshal(paramsJSON, &parametersMap)
		if parametersMap == nil {
			// Responses exige parameters; chat toleraba omitirlo (RV2-03).
			parametersMap = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		result = append(result, responses.ToolUnionParam{
			OfFunction: &responses.FunctionToolParam{
				Name:        fn.Name,
				Description: openai.String(fn.Description),
				Parameters:  parametersMap,
				// Responses asume strict=true si se omite; los schemas del
				// runtime no están escritos para strict.
				Strict: openai.Bool(false),
			},
		})
	}
	for _, tool := range tools {
		if tool.Type == "function" {
			add(tool.Function)
		}
	}
	for _, fn := range functions {
		add(fn)
	}
	return result
}

func convertToResponsesToolChoice(toolChoice any) responses.ResponseNewParamsToolChoiceUnion {
	mode := responses.ToolChoiceOptionsAuto
	if s, ok := toolChoice.(string); ok {
		switch s {
		case "none":
			mode = responses.ToolChoiceOptionsNone
		case "required":
			mode = responses.ToolChoiceOptionsRequired
		}
	}
	return responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: param.NewOpt(mode)}
}

func convertToResponsesTextFormat(format *llm.ResponseFormat) (responses.ResponseFormatTextConfigUnionParam, bool) {
	switch format.Type {
	case llm.JSONObject:
		return responses.ResponseFormatTextConfigUnionParam{OfJSONObject: &shared.ResponseFormatJSONObjectParam{}}, true
	case llm.JSONSchema:
		schema, ok := format.JSONSchema.(map[string]any)
		if !ok {
			b, _ := json.Marshal(format.JSONSchema)
			if err := json.Unmarshal(b, &schema); err != nil {
				return responses.ResponseFormatTextConfigUnionParam{}, false
			}
		}
		return responses.ResponseFormatTextConfigUnionParam{
			OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{Name: "schema", Schema: schema},
		}, true
	default:
		return responses.ResponseFormatTextConfigUnionParam{}, false
	}
}

// convertFromResponsesResponse arma el llm.Response con el mismo contrato de
// usage y metadata que convertFromOpenAIResponse (chat).
func convertFromResponsesResponse(resp *responses.Response) llm.Response {
	message := llm.Message{Role: llm.RoleAssistant}

	var finalText, allText, commentaryText strings.Builder
	hasPhase := false
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			text := outputMessageText(item)
			allText.WriteString(text)
			switch outputItemPhase(item) {
			case "final_answer":
				hasPhase = true
				finalText.WriteString(text)
			case "commentary":
				hasPhase = true
				commentaryText.WriteString(text)
			}
		case "function_call":
			// Con status "incomplete" los argumentos pueden venir cortados: no
			// se ejecuta ni se persiste (RV2-07/RV3-03).
			if item.Status != "" && item.Status != "completed" {
				continue
			}
			message.ToolCalls = append(message.ToolCalls, llm.ToolCall{
				ID:   item.CallID,
				Type: "function",
				Function: llm.FunctionCall{
					Name:      item.Name,
					Arguments: item.Arguments,
				},
			})
		}
	}
	switch {
	case !hasPhase:
		message.Content = allText.String()
	case finalText.Len() > 0:
		message.Content = finalText.String()
	case len(message.ToolCalls) == 0:
		// Solo commentary y sin tool calls: no perder la salida.
		message.Content = commentaryText.String()
	}

	promptTokens := int(resp.Usage.InputTokens)
	cachedPromptTokens := int(resp.Usage.InputTokensDetails.CachedTokens)
	if cachedPromptTokens < 0 {
		cachedPromptTokens = 0
	}
	if cachedPromptTokens > promptTokens {
		cachedPromptTokens = promptTokens
	}
	uncachedPromptTokens := promptTokens - cachedPromptTokens
	completionTokens := int(resp.Usage.OutputTokens)
	totalTokens := int(resp.Usage.TotalTokens)
	reasoningTokens := int(resp.Usage.OutputTokensDetails.ReasoningTokens)
	model := string(resp.Model)

	usage := llm.Usage{
		PromptTokens:         promptTokens,
		CachedPromptTokens:   cachedPromptTokens,
		UncachedPromptTokens: uncachedPromptTokens,
		CompletionTokens:     completionTokens,
		TotalTokens:          totalTokens,
		CacheWriteTokens:     parseResponsesCacheWriteTokens(resp.Usage.InputTokensDetails),
	}

	message.Metadata = map[string]any{
		"openai_model":                   model,
		"openai_prompt_tokens":           promptTokens,
		"openai_cached_prompt_tokens":    cachedPromptTokens,
		"openai_uncached_prompt_tokens":  uncachedPromptTokens,
		"openai_completion_tokens":       completionTokens,
		"openai_total_tokens":            totalTokens,
		"openai_reasoning_output_tokens": reasoningTokens,
		MetadataKeyOpenAIAPI:             MetadataValueOpenAIAPIResponses,
	}
	if resp.Status == responses.ResponseStatusIncomplete {
		reason := strings.TrimSpace(resp.IncompleteDetails.Reason)
		if reason == "" {
			reason = "unknown"
		}
		message.Metadata[MetadataKeyOpenAIIncompleteReason] = reason
	}

	return llm.Response{Model: model, Message: message, Usage: usage}
}

func outputMessageText(item responses.ResponseOutputItemUnion) string {
	var b strings.Builder
	for _, c := range item.Content {
		if c.Type == "output_text" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// outputItemPhase lee "phase" del JSON crudo del ítem (openai-go v3.8.1 no lo
// tipa). "" si no viene.
func outputItemPhase(item responses.ResponseOutputItemUnion) string {
	raw := item.RawJSON()
	if raw == "" {
		return ""
	}
	var v struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return ""
	}
	return v.Phase
}

// parseResponsesCacheWriteTokens lee usage.input_tokens_details.cache_write_tokens,
// que openai-go v3.8.1 no tipa. 0 si falta, es null o es inválido.
func parseResponsesCacheWriteTokens(details responses.ResponseUsageInputTokensDetails) int {
	field, ok := details.JSON.ExtraFields["cache_write_tokens"]
	if !ok {
		return 0
	}
	raw := strings.TrimSpace(field.Raw())
	if raw == "" || raw == "null" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 0)
	if err != nil || n < 0 {
		return 0
	}
	return int(n)
}

// reasoningReplayStore guarda, por call_id, los ítems reasoning que
// precedieron a esa tool call en la respuesta del modelo. Vive en memoria y
// solo lo que vive la instancia del proveedor.
type reasoningReplayStore struct {
	mu     sync.Mutex
	max    int
	byCall map[string]replayEntry
	order  []string
}

// replayEntry es lo que se reenvía para una tool call: los reasoning que la
// precedieron y el id (fc_…) del function_call en la respuesta original.
type replayEntry struct {
	reasoning []replayReasoningItem
	fcID      string
}

type replayReasoningItem struct {
	id               string
	encryptedContent string
}

func newReasoningReplayStore(max int) *reasoningReplayStore {
	return &reasoningReplayStore{max: max, byCall: make(map[string]replayEntry)}
}

func (s *reasoningReplayStore) remember(resp *responses.Response) {
	if s == nil || resp == nil {
		return
	}
	var items []replayReasoningItem
	var callIDs []string
	fcIDs := map[string]string{}
	for _, item := range resp.Output {
		switch item.Type {
		case "reasoning":
			// Sin encrypted_content el ítem no sirve con store=false (la API
			// lo ignora): no se guarda.
			if item.ID != "" && item.EncryptedContent != "" {
				items = append(items, replayReasoningItem{id: item.ID, encryptedContent: item.EncryptedContent})
			}
		case "function_call":
			if item.CallID != "" && (item.Status == "" || item.Status == "completed") {
				callIDs = append(callIDs, item.CallID)
				fcIDs[item.CallID] = item.ID
			}
		}
	}
	if len(items) == 0 || len(callIDs) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range callIDs {
		if _, exists := s.byCall[id]; !exists {
			s.order = append(s.order, id)
		}
		s.byCall[id] = replayEntry{reasoning: items, fcID: fcIDs[id]}
	}
	for len(s.order) > s.max {
		delete(s.byCall, s.order[0])
		s.order = s.order[1:]
	}
}

// itemsFor devuelve los ítems reasoning a reenviar antes de las tool calls de
// un mensaje assistant (los de la primera tool call que esté en el store) y,
// por call_id, el id del function_call original de las que estén en el store.
func (s *reasoningReplayStore) itemsFor(toolCalls []llm.ToolCall) ([]responses.ResponseInputItemUnionParam, map[string]string) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []responses.ResponseInputItemUnionParam
	fcIDs := map[string]string{}
	for _, tc := range toolCalls {
		entry, ok := s.byCall[tc.ID]
		if !ok {
			continue
		}
		if entry.fcID != "" {
			fcIDs[tc.ID] = entry.fcID
		}
		if out != nil {
			continue
		}
		out = make([]responses.ResponseInputItemUnionParam, 0, len(entry.reasoning))
		for _, it := range entry.reasoning {
			r := responses.ResponseInputItemParamOfReasoning(it.id, []responses.ResponseReasoningItemSummaryParam{})
			r.OfReasoning.EncryptedContent = openai.String(it.encryptedContent)
			out = append(out, r)
		}
	}
	return out, fcIDs
}
