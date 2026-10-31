package toolx

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/Conversia-AI/craftable-conversia/ai/llm"
)

type fakeTool struct {
	name    string
	version string
}

func (f fakeTool) Call(_ context.Context, _ string) (any, error) {
	return f.name + ":" + f.version, nil
}
func (f fakeTool) Name() string { return f.name }
func (f fakeTool) GetTool() llm.Tool {
	return llm.Tool{Type: "function", Function: llm.Function{Name: f.name, Description: f.version}}
}

func toolNames(tools []llm.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Function.Name)
	}
	return names
}

func manyTools() []Toolx {
	return []Toolx{
		fakeTool{name: "search_kb"}, fakeTool{name: "update_crm"}, fakeTool{name: "agenda"},
		fakeTool{name: "transfer"}, fakeTool{name: "quote"}, fakeTool{name: "zeta"},
		fakeTool{name: "beta"}, fakeTool{name: "lookup"},
	}
}

func TestFromToolxOrderedKeepsInputOrderAcross10000Calls(t *testing.T) {
	t.Parallel()

	client := FromToolxOrdered(manyTools()...)
	want := []string{"search_kb", "update_crm", "agenda", "transfer", "quote", "zeta", "beta", "lookup"}
	for i := 0; i < 10000; i++ {
		if got := toolNames(client.GetTools()); !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d: GetTools() = %v, want %v", i, got, want)
		}
	}
}

func TestFromToolxOrderedStableAcross10000Clients(t *testing.T) {
	t.Parallel()

	want := []string{"agenda", "beta", "lookup", "quote", "search_kb", "transfer", "update_crm", "zeta"}
	sorted := manyTools()
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name() < sorted[j].Name() })
	for i := 0; i < 10000; i++ {
		if got := toolNames(FromToolxOrdered(sorted...).GetTools()); !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d: GetTools() = %v, want %v", i, got, want)
		}
	}
}

// Dedup por Name() como FromToolx: gana la última definición, en la posición de
// la primera aparición.
func TestFromToolxOrderedDedup(t *testing.T) {
	t.Parallel()

	client := FromToolxOrdered(
		fakeTool{name: "a", version: "v1"},
		fakeTool{name: "b", version: "v1"},
		fakeTool{name: "a", version: "v2"},
		fakeTool{name: "c", version: "v1"},
		fakeTool{name: "b", version: "v2"},
	)

	got := client.GetTools()
	if names := toolNames(got); !reflect.DeepEqual(names, []string{"a", "b", "c"}) {
		t.Fatalf("GetTools() names = %v, want [a b c]", names)
	}
	versions := []string{got[0].Function.Description, got[1].Function.Description, got[2].Function.Description}
	if !reflect.DeepEqual(versions, []string{"v2", "v2", "v1"}) {
		t.Fatalf("GetTools() versions = %v, want [v2 v2 v1]", versions)
	}

	msg, err := client.Call(context.Background(), llm.ToolCall{ID: "call_1", Function: llm.FunctionCall{Name: "a"}})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if msg.Content != "a:v2" || msg.ToolCallID != "call_1" || msg.Role != llm.RoleTool {
		t.Fatalf("Call() = %+v, want the last definition of a", msg)
	}
}

// Con la misma entrada, el cliente ordenado y el no ordenado exponen las mismas tools.
func TestFromToolxOrderedSameSetAsFromToolx(t *testing.T) {
	t.Parallel()

	input := append(manyTools(), fakeTool{name: "agenda", version: "v2"})
	ordered := toolNames(FromToolxOrdered(input...).GetTools())
	unordered := toolNames(FromToolx(input...).GetTools())
	sort.Strings(ordered)
	sort.Strings(unordered)
	if !reflect.DeepEqual(ordered, unordered) {
		t.Fatalf("ordered set %v != FromToolx set %v", ordered, unordered)
	}
}

func TestFromToolxOrderedEmpty(t *testing.T) {
	t.Parallel()

	got := FromToolxOrdered().GetTools()
	if got == nil || len(got) != 0 {
		t.Fatalf("GetTools() = %#v, want an empty non-nil slice", got)
	}
	msg, err := FromToolxOrdered().Call(context.Background(), llm.ToolCall{ID: "x", Function: llm.FunctionCall{Name: "nope"}})
	if err != nil || msg.Content != "This tool dont exists" {
		t.Fatalf("Call() on empty client = %+v, %v", msg, err)
	}
}

// FromToolx conserva su comportamiento: no guarda orden, los duplicados quedan
// en la última definición y GetTools sigue iterando el map, así que en muchas
// llamadas produce más de un orden. Si esto empieza a fallar, FromToolx se volvió
// determinista por accidente (y cambiaron las requests de v1.4).
func TestFromToolxUnchanged(t *testing.T) {
	t.Parallel()

	client := FromToolx(append(manyTools(), fakeTool{name: "agenda", version: "v2"})...)
	if client.order != nil {
		t.Fatalf("FromToolx set order = %v, want nil", client.order)
	}
	if len(client.tools) != 8 {
		t.Fatalf("FromToolx kept %d tools, want 8", len(client.tools))
	}
	if client.tools["agenda"].(fakeTool).version != "v2" {
		t.Fatalf("FromToolx did not keep the last definition of a duplicate")
	}

	seen := map[string]struct{}{}
	for i := 0; i < 1000; i++ {
		names := toolNames(client.GetTools())
		if len(names) != 8 {
			t.Fatalf("GetTools() returned %d tools, want 8", len(names))
		}
		key := ""
		for _, n := range names {
			key += n + ","
		}
		seen[key] = struct{}{}
	}
	if len(seen) < 2 {
		t.Fatalf("FromToolx produced a single order over 1000 calls; map iteration no longer used")
	}

	msg, _ := client.Call(context.Background(), llm.ToolCall{ID: "c", Function: llm.FunctionCall{Name: "missing"}})
	if msg.Content != "This tool dont exists" {
		t.Fatalf("Call() unknown tool = %q", msg.Content)
	}
}
