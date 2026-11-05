package aiopenai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"github.com/Conversia-AI/craftable-conversia/ai/llm"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func TestParseCacheWriteTokens(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		details string // JSON de usage.prompt_tokens_details; "" = struct en cero
		want    int
	}{
		{name: "zero value struct", details: "", want: 0},
		{name: "present", details: `{"cached_tokens":5,"cache_write_tokens":7}`, want: 7},
		{name: "present zero", details: `{"cached_tokens":5,"cache_write_tokens":0}`, want: 0},
		{name: "absent", details: `{"cached_tokens":5}`, want: 0},
		{name: "empty object", details: `{}`, want: 0},
		{name: "null", details: `{"cache_write_tokens":null}`, want: 0},
		{name: "string", details: `{"cache_write_tokens":"7"}`, want: 0},
		{name: "float", details: `{"cache_write_tokens":7.5}`, want: 0},
		{name: "exponent", details: `{"cache_write_tokens":1e3}`, want: 0},
		{name: "negative", details: `{"cache_write_tokens":-3}`, want: 0},
		{name: "bool", details: `{"cache_write_tokens":true}`, want: 0},
		{name: "object", details: `{"cache_write_tokens":{"n":1}}`, want: 0},
		{name: "array", details: `{"cache_write_tokens":[1]}`, want: 0},
		{name: "overflow", details: `{"cache_write_tokens":99999999999999999999999}`, want: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var details openai.CompletionUsagePromptTokensDetails
			if tc.details != "" {
				if err := json.Unmarshal([]byte(tc.details), &details); err != nil {
					t.Fatalf("unmarshal %s: %v", tc.details, err)
				}
			}

			var got int
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("parseCacheWriteTokens panicked: %v", r)
					}
				}()
				got = parseCacheWriteTokens(details)
			}()
			if got != tc.want {
				t.Fatalf("parseCacheWriteTokens(%s) = %d, want %d", tc.details, got, tc.want)
			}
		})
	}
}

// Claves de Metadata que convertFromOpenAIResponse escribía antes de
// cache_write_tokens. El campo nuevo va solo a Usage, nunca a Metadata.
var wantMetadataKeys = []string{
	"openai_cached_prompt_tokens",
	"openai_completion_tokens",
	"openai_model",
	"openai_prompt_tokens",
	"openai_reasoning_output_tokens",
	"openai_total_tokens",
	"openai_uncached_prompt_tokens",
}

func TestChatReadsCacheWriteTokensFromResponse(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		details      string // valor crudo de prompt_tokens_details; "" = campo ausente
		wantWrites   int
		wantCached   int
		wantUncached int
	}{
		{name: "present", details: `{"cached_tokens":40,"cache_write_tokens":25}`, wantWrites: 25, wantCached: 40, wantUncached: 60},
		{name: "absent field", details: `{"cached_tokens":40}`, wantWrites: 0, wantCached: 40, wantUncached: 60},
		{name: "absent details", details: "", wantWrites: 0, wantCached: 0, wantUncached: 100},
		{name: "malformed string", details: `{"cached_tokens":40,"cache_write_tokens":"abc"}`, wantWrites: 0, wantCached: 40, wantUncached: 60},
		{name: "malformed object", details: `{"cached_tokens":40,"cache_write_tokens":{"x":1}}`, wantWrites: 0, wantCached: 40, wantUncached: 60},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			usage := `"usage":{"prompt_tokens":100,"completion_tokens":3,"total_tokens":103`
			if tc.details != "" {
				usage += `,"prompt_tokens_details":` + tc.details
			}
			usage += `}`

			_, resp := chatAgainst(t, `{"id":"chatcmpl-test","object":"chat.completion","created":0,"model":"gpt-5.6-luna",`+
				`"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],`+usage+`}`)

			if resp.Usage.CacheWriteTokens != tc.wantWrites {
				t.Fatalf("CacheWriteTokens = %d, want %d", resp.Usage.CacheWriteTokens, tc.wantWrites)
			}
			if resp.Usage.CachedPromptTokens != tc.wantCached || resp.Usage.UncachedPromptTokens != tc.wantUncached {
				t.Fatalf("cached/uncached = %d/%d, want %d/%d", resp.Usage.CachedPromptTokens, resp.Usage.UncachedPromptTokens, tc.wantCached, tc.wantUncached)
			}
			if resp.Usage.PromptTokens != 100 || resp.Usage.CompletionTokens != 3 || resp.Usage.TotalTokens != 103 {
				t.Fatalf("usage = %+v", resp.Usage)
			}

			keys := make([]string, 0, len(resp.Message.Metadata))
			for k := range resp.Message.Metadata {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if !reflect.DeepEqual(keys, wantMetadataKeys) {
				t.Fatalf("metadata keys = %v, want %v", keys, wantMetadataKeys)
			}
		})
	}
}

func TestChatPromptCacheKeyOnTheWire(t *testing.T) {
	t.Parallel()

	t.Run("not set is absent", func(t *testing.T) {
		t.Parallel()
		body := captureChatRequest(t, llm.WithModel("gpt-5.6-luna"), llm.WithUser("u-1"))
		if v, ok := body["prompt_cache_key"]; ok {
			t.Fatalf("prompt_cache_key = %v, want absent", v)
		}
		if body["user"] != "u-1" {
			t.Fatalf("user = %v, want u-1", body["user"])
		}
	})

	t.Run("empty is absent", func(t *testing.T) {
		t.Parallel()
		body := captureChatRequest(t, llm.WithModel("gpt-5.6-luna"), llm.WithPromptCacheKey(""))
		if v, ok := body["prompt_cache_key"]; ok {
			t.Fatalf("prompt_cache_key = %v, want absent", v)
		}
	})

	t.Run("set is sent", func(t *testing.T) {
		t.Parallel()
		body := captureChatRequest(t, llm.WithModel("gpt-5.6-luna"), llm.WithPromptCacheKey("agent-123"))
		if body["prompt_cache_key"] != "agent-123" {
			t.Fatalf("prompt_cache_key = %v, want agent-123", body["prompt_cache_key"])
		}
	})
}

// El request sin las opciones nuevas se valida byte a byte contra v1.0.13 en
// TestChatRequestBodyGoldenWithoutNewOptions (aiopenai_request_golden_test.go).

func TestChatStreamPromptCacheKeyOnTheWire(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		opts []llm.Option
		want string // "" = absent
	}{
		{name: "not set", opts: []llm.Option{llm.WithModel("gpt-5.6-luna")}},
		{name: "set", opts: []llm.Option{llm.WithModel("gpt-5.6-luna"), llm.WithPromptCacheKey("k-1")}, want: "k-1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bodyCh := make(chan map[string]any, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				var body map[string]any
				_ = json.Unmarshal(raw, &body)
				bodyCh <- body
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer srv.Close()

			provider := NewOpenAIProvider("test-key", option.WithBaseURL(srv.URL+"/"), option.WithMaxRetries(0))
			stream, err := provider.ChatStream(context.Background(), []llm.Message{llm.NewUserMessage("hola")}, tc.opts...)
			if err != nil {
				t.Fatalf("ChatStream() error = %v", err)
			}
			for {
				if _, err := stream.Next(); err != nil {
					break
				}
			}
			_ = stream.Close()

			body := <-bodyCh
			got, present := body["prompt_cache_key"]
			if tc.want == "" {
				if present {
					t.Fatalf("prompt_cache_key = %v, want absent", got)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("prompt_cache_key = %v, want %q", got, tc.want)
			}
		})
	}
}

func chatAgainst(t *testing.T, responseJSON string, opts ...llm.Option) (map[string]any, llm.Response) {
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
		_, _ = io.WriteString(w, responseJSON)
	}))
	defer srv.Close()

	provider := NewOpenAIProvider("test-key", option.WithBaseURL(srv.URL+"/"), option.WithMaxRetries(0))
	resp, err := provider.Chat(context.Background(), []llm.Message{llm.NewUserMessage("hola")}, opts...)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	return body, resp
}
