package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// Un CacheWriteTokens en 0 no cambia el Usage serializado: los logs JSON de
// proveedores que no reportan escrituras de cache quedan idénticos.
func TestUsageCacheWriteTokensOmitEmpty(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}`
	if string(raw) != want {
		t.Fatalf("json = %s, want %s", raw, want)
	}

	raw, err = json.Marshal(Usage{PromptTokens: 10, CacheWriteTokens: 4})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"cache_write_tokens":4`) {
		t.Fatalf("json = %s, want cache_write_tokens:4", raw)
	}
}

func TestWithPromptCacheKey(t *testing.T) {
	t.Parallel()

	opts := DefaultOptions()
	if opts.PromptCacheKey != "" {
		t.Fatalf("default PromptCacheKey = %q, want empty", opts.PromptCacheKey)
	}
	WithPromptCacheKey("agent-123")(opts)
	if opts.PromptCacheKey != "agent-123" {
		t.Fatalf("PromptCacheKey = %q, want agent-123", opts.PromptCacheKey)
	}
}
