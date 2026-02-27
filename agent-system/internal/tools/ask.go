package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// AskUserTool implements asking questions to the user
type AskUserTool struct{}

// AskUserParams represents parameters for asking questions
type AskUserParams struct {
	Questions []Question `json:"questions"`
}

// Question represents a single question to ask
type Question struct {
	Header   string   `json:"header"`
	Question string   `json:"question"`
	Options  []Option `json:"options"`
	Multiple bool     `json:"multiple,omitempty"`
}

// Option represents a single answer option
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// NewAskUserTool creates a new ask user tool
func NewAskUserTool() *AskUserTool {
	return &AskUserTool{}
}

func (t *AskUserTool) Name() string {
	return "ask_user_question"
}

func (t *AskUserTool) Description() string {
	return "Use this tool when you need to ask the user questions during execution. This allows you to: " +
		"1) Gather user preferences or requirements, 2) Clarify ambiguous instructions, 3) Get decisions on " +
		"implementation choices as you work, 4) Offer choices to the user about what direction to take."
}

func (t *AskUserTool) Schema() *ToolSchema {
	return &ToolSchema{
		Type: "object",
		Properties: map[string]Property{
			"questions": {
				Type:        "array",
				Description: "Questions to ask the user (1-4 questions)",
			},
		},
		Required: []string{"questions"},
	}
}

func (t *AskUserTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var askParams AskUserParams
	if err := json.Unmarshal(params, &askParams); err != nil {
		parsed, parseErr := parseAskUserParams(params)
		if parseErr != nil {
			return ToolResult{
				Success: false,
				Error:   fmt.Sprintf("failed to parse parameters: %v", err),
			}, nil
		}
		askParams = parsed
	}

	// Validate parameters
	if len(askParams.Questions) == 0 {
		return ToolResult{
			Success: false,
			Error:   "at least one question is required",
		}, nil
	}

	if len(askParams.Questions) > 4 {
		return ToolResult{
			Success: false,
			Error:   "maximum 4 questions allowed at once",
		}, nil
	}

	// Collect answers
	answers := make(map[string]interface{})

	reader := bufio.NewReader(os.Stdin)

	for i, q := range askParams.Questions {
		fmt.Printf("\n--- Question %d/%d ---\n", i+1, len(askParams.Questions))

		if q.Header != "" {
			fmt.Printf("[%s]\n", q.Header)
		}

		fmt.Printf("%s\n", q.Question)

		// Display options if provided
		if len(q.Options) > 0 {
			for j, opt := range q.Options {
				if opt.Description != "" {
					fmt.Printf("  %d. %s - %s\n", j+1, opt.Label, opt.Description)
				} else {
					fmt.Printf("  %d. %s\n", j+1, opt.Label)
				}
			}
			fmt.Println()
		}

		// Prompt user
		if q.Multiple && len(q.Options) > 0 {
			fmt.Print("Enter your choices (comma-separated numbers): ")
		} else if len(q.Options) > 0 {
			fmt.Print("Enter your choice (number): ")
		} else {
			fmt.Print("Your answer: ")
		}

		// Read response
		response, err := reader.ReadString('\n')
		if err != nil {
			return ToolResult{
				Success: false,
				Error:   fmt.Sprintf("failed to read user input: %v", err),
			}, nil
		}

		response = strings.TrimSpace(response)

		// Parse response
		if len(q.Options) > 0 {
			if q.Multiple {
				// Parse multiple selections
				var selected []string
				parts := strings.Split(response, ",")
				for _, part := range parts {
					part = strings.TrimSpace(part)
					num, err := strconv.Atoi(part)
					if err == nil && num >= 1 && num <= len(q.Options) {
						selected = append(selected, q.Options[num-1].Label)
					}
				}
				answers[q.Header] = selected
			} else {
				// Parse single selection
				num, err := strconv.Atoi(response)
				if err == nil && num >= 1 && num <= len(q.Options) {
					answers[q.Header] = q.Options[num-1].Label
				} else {
					// Fallback to free text
					answers[q.Header] = response
				}
			}
		} else {
			// Free text response
			answers[q.Header] = response
		}
	}

	// Build output
	var output strings.Builder
	output.WriteString("User responses:\n\n")

	for _, q := range askParams.Questions {
		output.WriteString(fmt.Sprintf("%s: %v\n", q.Header, answers[q.Header]))
	}

	return ToolResult{
		Success: true,
		Output:  output.String(),
		Data: map[string]interface{}{
			"answers":        answers,
			"question_count": len(askParams.Questions),
		},
	}, nil
}

func parseAskUserParams(params json.RawMessage) (AskUserParams, error) {
	var raw struct {
		Questions json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(params, &raw); err != nil {
		return AskUserParams{}, err
	}

	if len(raw.Questions) == 0 {
		return AskUserParams{}, fmt.Errorf("questions is required")
	}

	var questions []Question
	if err := json.Unmarshal(raw.Questions, &questions); err == nil {
		return AskUserParams{Questions: questions}, nil
	}

	var encoded string
	if err := json.Unmarshal(raw.Questions, &encoded); err != nil {
		return AskUserParams{}, err
	}

	if err := json.Unmarshal([]byte(encoded), &questions); err != nil {
		return AskUserParams{}, err
	}

	return AskUserParams{Questions: questions}, nil
}
