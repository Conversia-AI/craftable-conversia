package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/Conversia-AI/craftable-conversia/ai/llm"
	"github.com/Conversia-AI/craftable-conversia/ai/llm/agentx"
	"github.com/Conversia-AI/craftable-conversia/ai/llm/memoryx"
	"github.com/Conversia-AI/craftable-conversia/ai/llm/toolx"
	"github.com/Conversia-AI/craftable-conversia/ai/providers/aiclaude"
)

func main() {
	// Get API key from environment.
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		fmt.Println("Please set ANTHROPIC_API_KEY environment variable")
		os.Exit(1)
	}

	// Create the Claude provider and wrap it in the generic LLM client.
	provider := aiclaude.NewClaudeProvider(apiKey)
	client := llm.NewClient(provider)

	// Create a simple tool for tool-calling demos.
	weatherTool := NewWeatherTool()
	tools := toolx.FromToolx(weatherTool)

	// Create memory with a system prompt so the assistant keeps a stable role.
	mem := memoryx.NewMemory(memoryx.WithSystemPrompt("You are Claude, a helpful assistant that can check weather conditions."))

	// Create the agent using the shared LLM abstraction.
	myAgent := agentx.New(
		*client,
		mem,
		agentx.WithTools(tools),
		agentx.WithOptions(
			llm.WithToolChoice("required"),
			// llm.WithModel("claude-sonnet-4-5"),
			llm.WithMaxTokens(500),
			llm.WithTemperature(0.7),
		),
	)

	fmt.Println("=== Interactive Claude Weather Assistant ===")
	fmt.Println("Type your questions about weather (press Ctrl+C to exit)")
	fmt.Println("Example: What's the weather like in New York?")

	scanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Print("\n> ")
		if !scanner.Scan() {
			break
		}

		userQuery := scanner.Text()
		if strings.TrimSpace(userQuery) == "" {
			continue
		}

		response, err := myAgent.EvaluateWithTools(context.Background(), userQuery)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}

		fmt.Println("\nClaude:", response.FinalResponse)

		// Optional: uncomment this block if you want to inspect the execution trace.
		/*
			fmt.Println("\n--- Execution Steps ---")
			for i, step := range response.Steps {
				fmt.Printf("\nStep %d (%s):\n", i+1, step.StepType)

				if step.StepType == "initial" || step.StepType == "response" {
					fmt.Printf("LLM Output: %s\n", step.OutputMessage.Content)

					if len(step.OutputMessage.ToolCalls) > 0 {
						fmt.Println("Tool Calls:")
						for _, tc := range step.OutputMessage.ToolCalls {
							fmt.Printf("  - %s: %s\n", tc.Function.Name, tc.Function.Arguments)
						}
					}

					fmt.Printf("Tokens Used: %d\n", step.TokenUsage.TotalTokens)
				}

				if step.StepType == "tool_execution" {
					fmt.Println("Tool Responses:")
					for _, tr := range step.ToolResponses {
						fmt.Printf("  - %s\n", tr.Content)
					}
				}
			}
		*/
	}

	fmt.Println("\nExiting. Goodbye!")
}

// WeatherTool provides weather information.
type WeatherTool struct{}

type WeatherRequest struct {
	Location string `json:"location"`
}

func NewWeatherTool() *WeatherTool {
	return &WeatherTool{}
}

func (w *WeatherTool) Name() string {
	return "get_weather"
}

func (w *WeatherTool) GetTool() llm.Tool {
	return llm.Tool{
		Type: "function",
		Function: llm.Function{
			Name:        w.Name(),
			Description: "Get the current weather in a location",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"location": map[string]any{
						"type":        "string",
						"description": "The city name, e.g. New York",
					},
				},
				"required": []string{"location"},
			},
		},
	}
}

func (w *WeatherTool) Call(ctx context.Context, inputs string) (any, error) {
	var request WeatherRequest
	if err := json.Unmarshal([]byte(inputs), &request); err != nil {
		return nil, fmt.Errorf("failed to parse weather request: %w", err)
	}

	weatherData := fmt.Sprintf("Currently 22°C and partly cloudy in %s.", request.Location)
	return weatherData, nil
}
