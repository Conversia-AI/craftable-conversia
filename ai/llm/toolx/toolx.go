package toolx

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/Conversia-AI/craftable-conversia/ai/llm"
)

type Toolx interface {
	Call(ctx context.Context, inputs string) (any, error)
	GetTool() llm.Tool
	Name() string
}

type ToolxClient struct {
	tools map[string]Toolx
	// order es nil en los clientes creados con FromToolx (GetTools sigue
	// iterando el map, así que su orden no está definido). FromToolxOrdered lo
	// llena con los nombres únicos en el orden en que GetTools debe devolverlos.
	order []string
}

func FromToolx(tools ...Toolx) *ToolxClient {
	toolMap := make(map[string]Toolx)
	for _, tool := range tools {
		toolMap[tool.Name()] = tool
	}
	return &ToolxClient{tools: toolMap}
}

// FromToolxOrdered crea un cliente cuyo GetTools devuelve las tools en orden
// determinista: el de la primera aparición de cada Name() en tools. Los
// duplicados se resuelven como en FromToolx: gana la última definición con ese
// Name(), pero conserva la posición de la primera aparición. Quien quiera un
// orden canónico (p. ej. por nombre) tiene que ordenar tools antes de llamarla.
func FromToolxOrdered(tools ...Toolx) *ToolxClient {
	toolMap := make(map[string]Toolx, len(tools))
	order := make([]string, 0, len(tools))
	for _, tool := range tools {
		name := tool.Name()
		if _, seen := toolMap[name]; !seen {
			order = append(order, name)
		}
		toolMap[name] = tool
	}
	return &ToolxClient{tools: toolMap, order: order}
}

func (t *ToolxClient) GetTools() []llm.Tool {
	if t.order == nil {
		tools := make([]llm.Tool, 0, len(t.tools))
		for _, tool := range t.tools {
			tools = append(tools, tool.GetTool())
		}
		return tools
	}
	tools := make([]llm.Tool, 0, len(t.order))
	for _, name := range t.order {
		tools = append(tools, t.tools[name].GetTool())
	}
	return tools
}

func (t *ToolxClient) Call(ctx context.Context, tc llm.ToolCall) (llm.Message, error) {
	tool, ok := t.tools[tc.Function.Name]
	if !ok {
		return llm.NewToolMessage(tc.ID, "This tool dont exists"), nil // create custom errors for this
	}

	result, err := tool.Call(ctx, tc.Function.Arguments)
	if err != nil {
		return llm.NewToolMessage(tc.ID, "Error calling tool: "+err.Error()), nil //create a custom error for this
	}

	var resultStr string
	switch v := result.(type) {
	case string:
		resultStr = v
	case []byte:
		resultStr = string(v)
	case int:
		resultStr = strconv.Itoa(v)
	case float64:
		resultStr = strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		resultStr = strconv.FormatBool(v)
	case fmt.Stringer:
		resultStr = v.String()
	default:
		// Use JSON marshaling for complex types
		jsonBytes, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return llm.NewToolMessage(tc.ID, "Error converting result to string: "+jsonErr.Error()), nil //create a custom error for this
		}
		resultStr = string(jsonBytes)
	}
	return llm.NewToolMessage(tc.ID, resultStr), nil
}
