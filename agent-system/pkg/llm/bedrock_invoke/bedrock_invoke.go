package bedrock_invoke

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"agent-system/internal/config"
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

	isOpenAICompat := strings.Contains(m.modelConfig.ModelID, "nemotron") || strings.Contains(m.modelConfig.ModelID, "openai")

	// If the model is Nemotron or OpenAI-compatible (e.g. GPT-OSS), use OpenAI format
	if isOpenAICompat {
		type oaiCompatMessage struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		type oaiCompatRequest struct {
			Messages    []oaiCompatMessage `json:"messages"`
			MaxTokens   int                `json:"max_tokens,omitempty"`
			Temperature float64            `json:"temperature,omitempty"`
		}

		var oaiMessages []oaiCompatMessage
		if systemPrompt != "" {
			oaiMessages = append(oaiMessages, oaiCompatMessage{Role: "system", Content: systemPrompt})
		}
		for _, msg := range bedrockMessages {
			content := ""
			for _, c := range msg.Content {
				content += c.Text
			}
			oaiMessages = append(oaiMessages, oaiCompatMessage{
				Role:    msg.Role,
				Content: content,
			})
		}

		oaiReq := oaiCompatRequest{
			Messages:    oaiMessages,
			MaxTokens:   opts.MaxTokens,
			Temperature: opts.Temperature,
		}

		reqBody, err = json.Marshal(oaiReq)
	} else {
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
		reqBody, err = json.Marshal(novaReq)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	if os.Getenv("DEBUG_BEDROCK") == "1" {
		fmt.Printf("DEBUG: Bedrock Request Body: %s\n", string(reqBody))
	}

	input := &bedrockruntime.InvokeModelInput{
		ModelId:     &m.modelConfig.ModelID,
		ContentType: stringPtr("application/json"),
		Accept:      stringPtr("application/json"),
		Body:        reqBody,
	}

	result, err := m.client.InvokeModel(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to invoke model: %w", err)
	}

	var content string
	var inputTokens, outputTokens int

	if os.Getenv("DEBUG_BEDROCK") == "1" {
		fmt.Printf("DEBUG: Bedrock Response Body: %s\n", string(result.Body))
	}

	if isOpenAICompat {
		// Nemotron/GPT-OSS returns an OpenAI-compatible response
		type openAIResponse struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		var oaiResp openAIResponse
		if err := json.Unmarshal(result.Body, &oaiResp); err != nil {
			return nil, fmt.Errorf("failed to unmarshal nemotron response: %w. Body: %s", err, string(result.Body))
		}
		if len(oaiResp.Choices) > 0 {
			content = oaiResp.Choices[0].Message.Content
		}
		inputTokens = oaiResp.Usage.PromptTokens
		outputTokens = oaiResp.Usage.CompletionTokens
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
	Messages        []novaMessage       `json:"messages"`
	InferenceConfig *novaInferenceConfig `json:"inferenceConfig,omitempty"`
	System          []novaContent       `json:"system,omitempty"`
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
