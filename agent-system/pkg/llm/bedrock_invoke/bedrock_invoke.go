package bedrock_invoke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/smithy-go"

	"agent-system/internal/config"
	"agent-system/internal/prompts"
	"agent-system/internal/usage"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/tmc/langchaingo/llms"
)

// BedrockInvokeModel implements llms.Model using AWS Bedrock InvokeModel API directly
// for models that are not well supported by langchaingo's bedrock provider.
type BedrockInvokeModel struct {
	client      *bedrockruntime.Client
	modelConfig config.ModelConfig
}

func New(modelConfig config.ModelConfig) (*BedrockInvokeModel, error) {
	ctx := context.Background()
	awsOpts := []func(*awsconfig.LoadOptions) error{}
	if modelConfig.Region != "" {
		awsOpts = append(awsOpts, awsconfig.WithRegion(modelConfig.Region))
	}
	if modelConfig.AWSProfile != "" {
		awsOpts = append(awsOpts, awsconfig.WithSharedConfigProfile(modelConfig.AWSProfile))
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &BedrockInvokeModel{
		client:      bedrockruntime.NewFromConfig(cfg),
		modelConfig: modelConfig,
	}, nil
}

func (m *BedrockInvokeModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	opts := &llms.CallOptions{}
	for _, opt := range options {
		opt(opts)
	}

	if opts.Temperature == 0 {
		opts.Temperature = float64(m.modelConfig.Temperature)
	}
	if opts.MaxTokens == 0 {
		opts.MaxTokens = m.modelConfig.MaxTokens
	}
	if opts.MaxTokens == 0 {
		opts.MaxTokens = 2048
	}

	var systemPrompt string
	var bedrockMessages []novaMessage

	for _, mc := range messages {
		role := ""
		switch mc.Role {
		case llms.ChatMessageTypeSystem:
			for _, p := range mc.Parts {
				if t, ok := p.(llms.TextContent); ok {
					systemPrompt += t.Text
				}
			}
			continue
		case llms.ChatMessageTypeHuman:
			role = "user"
		case llms.ChatMessageTypeAI:
			role = "assistant"
		case llms.ChatMessageTypeTool:
			role = "user" // Treat tool results as user messages in Nova
		default:
			continue
		}

		var content []novaContent
		for _, p := range mc.Parts {
			switch t := p.(type) {
			case llms.TextContent:
				content = append(content, novaContent{Text: t.Text})
			case llms.ToolCallResponse:
				content = append(content, novaContent{Text: fmt.Sprintf("Tool Result (%s): %s", t.Name, t.Content)})
			case llms.ToolCall:
				content = append(content, novaContent{Text: fmt.Sprintf("I want to call tool %s with arguments %s", t.FunctionCall.Name, t.FunctionCall.Arguments)})
			}
		}

		if len(content) > 0 {
			// Consolidate with previous message if same role
			if len(bedrockMessages) > 0 && bedrockMessages[len(bedrockMessages)-1].Role == role {
				bedrockMessages[len(bedrockMessages)-1].Content = append(bedrockMessages[len(bedrockMessages)-1].Content, content...)
			} else {
				bedrockMessages = append(bedrockMessages, novaMessage{
					Role:    role,
					Content: content,
				})
			}
		}
	}

	// Ensure alternating roles: user, assistant, user, assistant...
	// If it starts with assistant, prepend a dummy user message
	if len(bedrockMessages) > 0 && bedrockMessages[0].Role == "assistant" {
		bedrockMessages = append([]novaMessage{{Role: "user", Content: []novaContent{{Text: "Hello"}}}}, bedrockMessages...)
	}

	// Nova requires messages to alternate role. If we have consecutive roles, consolidate them (already done above)
	// Also ensure it ends with a user message if we are expecting an assistant response
	// Actually Bedrock/Nova usually requires the last message to be 'user' for InvokeModel
	if len(bedrockMessages) > 0 && bedrockMessages[len(bedrockMessages)-1].Role == "assistant" {
		// This shouldn't happen in a normal chat loop where we wait for AI response,
		// but if it does, Nova might complain.
	}

	var reqBody []byte
	var err error

	// Determine format: use config Format field, or guess from model ID, default to openai
	format := m.modelConfig.Format
	if format == "" {
		// Backward compatibility: guess from model ID
		if strings.Contains(m.modelConfig.ModelID, "nemotron") || strings.Contains(m.modelConfig.ModelID, "openai") {
			format = "openai"
		} else if strings.Contains(m.modelConfig.ModelID, "nova") {
			format = "nova"
		} else {
			format = "openai" // Default
		}
	}
	isOpenAICompat := format == "openai"

	// If the model is Nemotron or OpenAI-compatible (e.g. GPT-OSS), use OpenAI format
	if isOpenAICompat {
		type oaiContent struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		type oaiCompatMessage struct {
			Role    string       `json:"role"`
			Content []oaiContent `json:"content"`
		}
		type oaiCompatTool struct {
			Type     string                 `json:"type"`
			Function map[string]interface{} `json:"function"`
		}
		type oaiCompatRequest struct {
			Messages    []oaiCompatMessage `json:"messages"`
			MaxTokens   int                `json:"max_tokens,omitempty"`
			Temperature float64            `json:"temperature,omitempty"`
			Tools       []oaiCompatTool    `json:"tools,omitempty"`
			ToolChoice  interface{}        `json:"tool_choice,omitempty"`
		}

		var oaiMessages []oaiCompatMessage
		if systemPrompt != "" {
			oaiMessages = append(oaiMessages, oaiCompatMessage{
				Role:    "system",
				Content: []oaiContent{{Type: "text", Text: systemPrompt}},
			})
		}
		for _, msg := range bedrockMessages {
			var contents []oaiContent
			for _, c := range msg.Content {
				contents = append(contents, oaiContent{Type: "text", Text: c.Text})
			}
			oaiMessages = append(oaiMessages, oaiCompatMessage{
				Role:    msg.Role,
				Content: contents,
			})
		}

		oaiReq := oaiCompatRequest{
			Messages:    oaiMessages,
			MaxTokens:   opts.MaxTokens,
			Temperature: opts.Temperature,
		}

		// Add tools if provided
		if len(opts.Tools) > 0 {
			for _, tool := range opts.Tools {
				if tool.Type == "function" && tool.Function != nil {
					funcDef := map[string]interface{}{
						"name":        tool.Function.Name,
						"description": tool.Function.Description,
					}
					if tool.Function.Parameters != nil {
						funcDef["parameters"] = tool.Function.Parameters
					}
					oaiReq.Tools = append(oaiReq.Tools, oaiCompatTool{
						Type:     "function",
						Function: funcDef,
					})
				}
			}
			if opts.ToolChoice != nil {
				oaiReq.ToolChoice = opts.ToolChoice
			}
		}

		reqBody, err = json.Marshal(oaiReq)
	} else {
		// Nova format with toolConfig
		type novaToolInputSchema struct {
			JSON map[string]interface{} `json:"json"`
		}
		type novaToolSpec struct {
			Name        string              `json:"name"`
			Description string              `json:"description"`
			InputSchema novaToolInputSchema `json:"inputSchema"`
		}
		type novaTool struct {
			ToolSpec novaToolSpec `json:"toolSpec"`
		}
		type novaToolConfig struct {
			Tools      []novaTool  `json:"tools"`
			ToolChoice interface{} `json:"toolChoice,omitempty"`
		}

		novaReq := novaRequest{
			Messages: bedrockMessages,
			InferenceConfig: &novaInferenceConfig{
				MaxNewTokens: opts.MaxTokens,
				Temperature:  opts.Temperature,
			},
		}

		if systemPrompt != "" {
			novaReq.System = []novaContent{{Text: systemPrompt}}
		}

		// Add tools if provided
		if len(opts.Tools) > 0 {
			var tools []novaTool
			for _, tool := range opts.Tools {
				if tool.Type == "function" && tool.Function != nil {
					schema := map[string]interface{}{
						"type":       "object",
						"properties": map[string]interface{}{},
					}
					if tool.Function.Parameters != nil {
						// Convert parameters to map
						paramsJSON, _ := json.Marshal(tool.Function.Parameters)
						var paramsMap map[string]interface{}
						json.Unmarshal(paramsJSON, &paramsMap)
						schema = paramsMap
					}
					tools = append(tools, novaTool{
						ToolSpec: novaToolSpec{
							Name:        tool.Function.Name,
							Description: tool.Function.Description,
							InputSchema: novaToolInputSchema{JSON: schema},
						},
					})
				}
			}
			if len(tools) > 0 {
				toolConfig := novaToolConfig{Tools: tools}
				if opts.ToolChoice != nil {
					toolConfig.ToolChoice = opts.ToolChoice
				}
				// Marshal toolConfig and add to request
				novaReq.ToolConfig = &toolConfig
			}
		}

		reqBody, err = json.Marshal(novaReq)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	if os.Getenv("DEBUG_BEDROCK") == "1" {
		fmt.Printf("DEBUG: Bedrock Request Body: %s\n", string(reqBody))
	}

	callID := time.Now().UnixMilli()
	callStartTime := time.Now()

	input := &bedrockruntime.InvokeModelInput{
		ModelId:     &m.modelConfig.ModelID,
		ContentType: stringPtr("application/json"),
		Accept:      stringPtr("application/json"),
		Body:        reqBody,
	}

	// Dump verbose logs if enabled
	if prompts.IsVerbose() {
		dumpDir := "/tmp/bedrock-invoke"
		if err := os.MkdirAll(dumpDir, 0755); err == nil {
			// Build free-form request dump
			var reqDump strings.Builder
			reqDump.WriteString(fmt.Sprintf("Call ID: %d\n", callID))
			reqDump.WriteString(fmt.Sprintf("Timestamp: %s\n", callStartTime.UTC().Format(time.RFC3339Nano)))
			reqDump.WriteString(fmt.Sprintf("Model ID: %s\n", m.modelConfig.ModelID))
			reqDump.WriteString(fmt.Sprintf("Region: %s\n", m.modelConfig.Region))
			reqDump.WriteString(fmt.Sprintf("AWS Profile: %s\n", m.modelConfig.AWSProfile))
			reqDump.WriteString(fmt.Sprintf("Format: %s\n", format))
			reqDump.WriteString(fmt.Sprintf("Temperature: %.2f\n", opts.Temperature))
			reqDump.WriteString(fmt.Sprintf("Max Tokens: %d\n", opts.MaxTokens))
			reqDump.WriteString(fmt.Sprintf("Number of Tools: %d\n", len(opts.Tools)))
			if len(opts.Tools) > 0 {
				reqDump.WriteString("\n--- Tools ---\n")
				for i, tool := range opts.Tools {
					reqDump.WriteString(fmt.Sprintf("Tool %d:\n", i))
					reqDump.WriteString(fmt.Sprintf("  Type: %s\n", tool.Type))
					if tool.Function != nil {
						reqDump.WriteString(fmt.Sprintf("  Name: %s\n", tool.Function.Name))
						reqDump.WriteString(fmt.Sprintf("  Description: %s\n", tool.Function.Description))
						if tool.Function.Parameters != nil {
							paramsJSON, _ := json.MarshalIndent(tool.Function.Parameters, "    ", "  ")
							reqDump.WriteString(fmt.Sprintf("  Parameters:\n    %s\n", string(paramsJSON)))
						}
					}
				}
			}
			if opts.ToolChoice != nil {
				reqDump.WriteString(fmt.Sprintf("Tool Choice: %v\n", opts.ToolChoice))
			}
			reqDump.WriteString("\n--- Request Body (JSON) ---\n")
			reqDump.WriteString(string(reqBody))
			reqDump.WriteString("\n\n--- InvokeModelInput ---\n")
			reqDump.WriteString(fmt.Sprintf("ModelId: %s\n", *input.ModelId))
			reqDump.WriteString(fmt.Sprintf("ContentType: %s\n", *input.ContentType))
			reqDump.WriteString(fmt.Sprintf("Accept: %s\n", *input.Accept))
			reqDump.WriteString(fmt.Sprintf("Body length: %d bytes\n", len(input.Body)))

			reqFile := filepath.Join(dumpDir, fmt.Sprintf("%d.request", callID))
			os.WriteFile(reqFile, []byte(reqDump.String()), 0644)
		}
	}

	// Retry configuration for transient errors
	const maxRetries = 3
	const baseDelay = 500 * time.Millisecond

	var result *bedrockruntime.InvokeModelOutput
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			delay := baseDelay * time.Duration(1<<uint(attempt-1)) // exponential backoff
			fmt.Fprintf(os.Stderr, "Retrying InvokeModel after %v (attempt %d/%d, request size: %d bytes)\n", delay, attempt+1, maxRetries, len(reqBody))
			time.Sleep(delay)
		}

		result, lastErr = m.client.InvokeModel(ctx, input)
		if lastErr == nil {
			break
		}

		// Check if error is a 5xx server error (retryable)
		var apiErr smithy.APIError
		if errors.As(lastErr, &apiErr) {
			statusCode := 0
			if httpErr, ok := lastErr.(interface{ HTTPStatusCode() int }); ok {
				statusCode = httpErr.HTTPStatusCode()
			}

			// Log detailed error info with context
			if statusCode >= 500 {
				fmt.Fprintf(os.Stderr, "InvokeModel failed with status %d (attempt %d/%d): %v\n", statusCode, attempt+1, maxRetries, lastErr)
				fmt.Fprintf(os.Stderr, "Request context: model=%s, region=%s, request_size=%d bytes\n", m.modelConfig.ModelID, m.modelConfig.Region, len(reqBody))
				continue // retry
			}

			// Non-5xx errors are not retryable
			return nil, fmt.Errorf("API returned unexpected status code: %d (request_size=%d bytes): %w", statusCode, len(reqBody), lastErr)
		}

		// Non-API errors (network, etc.) - retry
		fmt.Fprintf(os.Stderr, "InvokeModel failed (attempt %d/%d): %v\n", attempt+1, maxRetries, lastErr)
	}

	if lastErr != nil {
		return nil, fmt.Errorf("failed to invoke model after %d attempts (request_size=%d bytes): %w", maxRetries, len(reqBody), lastErr)
	}

	callDuration := time.Since(callStartTime)

	var content string
	var inputTokens, outputTokens int

	if os.Getenv("DEBUG_BEDROCK") == "1" {
		fmt.Printf("DEBUG: Bedrock Response Body: %s\n", string(result.Body))
	}

	// Dump response to same file if verbose
	if prompts.IsVerbose() {
		dumpDir := "/tmp/bedrock-invoke"
		respTime := time.Now()

		var respDump strings.Builder
		respDump.WriteString("\n\n===== RESPONSE =====\n\n")
		respDump.WriteString(fmt.Sprintf("Response Timestamp: %s\n", respTime.UTC().Format(time.RFC3339Nano)))
		respDump.WriteString(fmt.Sprintf("Call Duration: %s (%.3f seconds)\n", callDuration, callDuration.Seconds()))
		respDump.WriteString("\n--- Response Body (JSON) ---\n")
		respDump.WriteString(string(result.Body))

		// Append to the same request file
		reqFile := filepath.Join(dumpDir, fmt.Sprintf("%d.request", callID))
		f, err := os.OpenFile(reqFile, os.O_APPEND|os.O_WRONLY, 0644)
		if err == nil {
			f.WriteString(respDump.String())
			f.Close()
		}
	}

	if isOpenAICompat {
		// Nemotron/GPT-OSS returns an OpenAI-compatible response
		type openAIFunction struct {
			Arguments string `json:"arguments"`
			Name      string `json:"name"`
		}
		type openAIToolCall struct {
			Function openAIFunction `json:"function"`
			ID       string         `json:"id"`
			Type     string         `json:"type"`
		}
		type openAIMessage struct {
			Content   string           `json:"content"`
			ToolCalls []openAIToolCall `json:"tool_calls,omitempty"`
			Role      string           `json:"role"`
		}
		type openAIChoice struct {
			FinishReason string        `json:"finish_reason"`
			Message      openAIMessage `json:"message"`
		}
		type openAIResponse struct {
			Choices []openAIChoice `json:"choices"`
			Usage   struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		var oaiResp openAIResponse
		if err := json.Unmarshal(result.Body, &oaiResp); err != nil {
			return nil, fmt.Errorf("failed to unmarshal nemotron response: %w. Body: %s", err, string(result.Body))
		}
		if len(oaiResp.Choices) > 0 {
			choice := oaiResp.Choices[0]
			content = choice.Message.Content
			inputTokens = oaiResp.Usage.PromptTokens
			outputTokens = oaiResp.Usage.CompletionTokens

			// Build ContentChoice with tool calls
			contentChoice := &llms.ContentChoice{
				Content: content,
				GenerationInfo: map[string]interface{}{
					"Usage": usage.Usage{
						InputTokens:  inputTokens,
						OutputTokens: outputTokens,
					},
				},
			}

			// Parse tool calls
			for _, tc := range choice.Message.ToolCalls {
				if tc.Type == "function" {
					contentChoice.ToolCalls = append(contentChoice.ToolCalls, llms.ToolCall{
						ID:   tc.ID,
						Type: tc.Type,
						FunctionCall: &llms.FunctionCall{
							Name:      tc.Function.Name,
							Arguments: tc.Function.Arguments,
						},
					})
				}
			}

			// Set legacy FuncCall for backwards compatibility
			if len(contentChoice.ToolCalls) > 0 {
				contentChoice.FuncCall = contentChoice.ToolCalls[0].FunctionCall
			}

			return &llms.ContentResponse{
				Choices: []*llms.ContentChoice{contentChoice},
			}, nil
		}
	} else {
		var novaResp novaResponse
		if err := json.Unmarshal(result.Body, &novaResp); err != nil {
			return nil, fmt.Errorf("failed to unmarshal nova response: %w", err)
		}
		for _, c := range novaResp.Output.Message.Content {
			content += c.Text
		}
		inputTokens = novaResp.Usage.InputTokens
		outputTokens = novaResp.Usage.OutputTokens
	}

	resp := &llms.ContentResponse{
		Choices: []*llms.ContentChoice{
			{
				Content: content,
				GenerationInfo: map[string]interface{}{
					"Usage": usage.Usage{
						InputTokens:  inputTokens,
						OutputTokens: outputTokens,
					},
				},
			},
		},
	}

	return resp, nil
}

func (m *BedrockInvokeModel) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	resp, err := m.GenerateContent(ctx, []llms.MessageContent{llms.TextParts(llms.ChatMessageTypeHuman, prompt)}, options...)
	if err != nil {
		return "", err
	}
	return resp.Choices[0].Content, nil
}

// Internal structures for Nova
type novaRequest struct {
	Messages        []novaMessage        `json:"messages"`
	InferenceConfig *novaInferenceConfig `json:"inferenceConfig,omitempty"`
	System          []novaContent        `json:"system,omitempty"`
	ToolConfig      interface{}          `json:"toolConfig,omitempty"`
}

type novaMessage struct {
	Role    string        `json:"role"`
	Content []novaContent `json:"content"`
}

type novaContent struct {
	Text string `json:"text,omitempty"`
}

type novaInferenceConfig struct {
	MaxNewTokens int     `json:"max_new_tokens,omitempty"`
	Temperature  float64 `json:"temperature,omitempty"`
}

type novaResponse struct {
	Output struct {
		Message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	} `json:"output"`
	Usage struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
		TotalTokens  int `json:"totalTokens"`
	} `json:"usage"`
}

func stringPtr(s string) *string { return &s }
