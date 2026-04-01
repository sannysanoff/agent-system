package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"agent-system/internal/prompts"
	"gopkg.in/yaml.v3"
)

// ModelConfig represents configuration for a single model
type ModelConfig struct {
	Name         string            `yaml:"name"`
	Provider     string            `yaml:"provider"`
	ModelID      string            `yaml:"model_id"`
	APIKey       string            `yaml:"api_key"`
	APIKeyEnv    string            `yaml:"api_key_env"`
	BaseURL      string            `yaml:"base_url,omitempty"`
	MaxTokens    int               `yaml:"max_tokens,omitempty"`
	Temperature  float32           `yaml:"temperature,omitempty"`
	ExtraHeaders map[string]string `yaml:"extra_headers,omitempty"`
	Region       string            `yaml:"region,omitempty"`        // For Bedrock
	AccessKey    string            `yaml:"access_key,omitempty"`    // For Bedrock
	SecretKey    string            `yaml:"secret_key,omitempty"`    // For Bedrock
	SessionToken string            `yaml:"session_token,omitempty"` // For Bedrock
	AWSProfile   string            `yaml:"aws_profile,omitempty"`   // For Bedrock
	SoftTools    bool              `yaml:"soft_tools,omitempty"`    // Enable soft tools mode
	CachePoints  bool              `yaml:"cache_points,omitempty"`  // Enable cache points for Nova models
	Format       string            `yaml:"format,omitempty"`        // Request format: "openai" or "nova" (default: openai)
}

// AgentConfig represents the main configuration structure
type AgentConfig struct {
	Variables    map[string]string      `yaml:"variables"`
	DefaultModel string                 `yaml:"default_model"`
	Models       map[string]ModelConfig `yaml:"models"`
	Tools        ToolConfig             `yaml:"tools"`
}

// ToolConfig represents tool-specific configuration
type ToolConfig struct {
	Bash struct {
		Enabled          bool     `yaml:"enabled"`
		DefaultTimeout   int      `yaml:"default_timeout_ms"`
		AllowedCommands  []string `yaml:"allowed_commands,omitempty"`
		BlockedCommands  []string `yaml:"blocked_commands,omitempty"`
		WorkingDirectory string   `yaml:"working_directory,omitempty"`
	} `yaml:"bash"`

	Grep struct {
		Enabled         bool `yaml:"enabled"`
		MaxResults      int  `yaml:"max_results,omitempty"`
		MaxContextLines int  `yaml:"max_context_lines,omitempty"`
	} `yaml:"grep"`

	Glob struct {
		Enabled    bool `yaml:"enabled"`
		MaxResults int  `yaml:"max_results,omitempty"`
	} `yaml:"glob"`

	Read struct {
		Enabled   bool `yaml:"enabled"`
		ReadLimit int  `yaml:"read_limit,omitempty"`
	} `yaml:"read"`

	Write struct {
		Enabled bool `yaml:"enabled"`
	} `yaml:"write"`

	Edit struct {
		Enabled bool `yaml:"enabled"`
	} `yaml:"edit"`

	Task struct {
		Enabled       bool     `yaml:"enabled"`
		DefaultModel  string   `yaml:"default_model"`
		AllowedAgents []string `yaml:"allowed_agents,omitempty"`
		MaxConcurrent int      `yaml:"max_concurrent,omitempty"`
	} `yaml:"task"`

	AskUser struct {
		Enabled bool `yaml:"enabled"`
	} `yaml:"ask_user"`

	WebFetch struct {
		Enabled     bool `yaml:"enabled"`
		TimeoutSecs int  `yaml:"timeout_secs,omitempty"`
	} `yaml:"webfetch"`

	WebSearch struct {
		Enabled     bool `yaml:"enabled"`
		MaxResults  int  `yaml:"max_results,omitempty"`
		TimeoutSecs int  `yaml:"timeout_secs,omitempty"`
	} `yaml:"websearch"`

	Skill struct {
		Enabled bool `yaml:"enabled"`
	} `yaml:"skill"`
}

// LoadConfig loads configuration from YAML files
func LoadConfig(path string) (*AgentConfig, error) {
	localDir, _ := os.Getwd()
	appDir := filepath.Dir(path)
	baseName := filepath.Base(path)
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		appDir = filepath.Dir(exe)
	}
	if baseName == "." || baseName == "" {
		baseName = "config.yaml"
	}

	// Get user config directory
	var userConfigDir string
	if homeDir, err := os.UserHomeDir(); err == nil {
		userConfigDir = filepath.Join(homeDir, ".myagent")
	}

	prompts.VerboseLog("config search: local_dir=%q app_dir=%q user_dir=%q base_name=%q", localDir, appDir, userConfigDir, baseName)

	basePath := findConfigPath(localDir, appDir, userConfigDir, baseName)
	overridePath := findConfigPath(localDir, appDir, userConfigDir, "config.local.yaml")
	if basePath == "" && overridePath == "" {
		dirs := []string{localDir}
		if userConfigDir != "" {
			dirs = append(dirs, userConfigDir)
		}
		dirs = append(dirs, appDir)
		return nil, fmt.Errorf("failed to read config file: %s not found in %s", baseName, strings.Join(dirs, ", "))
	}

	var mergedData []byte
	if basePath != "" {
		prompts.VerboseLog("config base found: %q", basePath)
		baseData, err := os.ReadFile(basePath)
		if err != nil {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
		mergedData, err = mergeConfigData(baseData, localDir, appDir, userConfigDir)
		if err != nil {
			return nil, err
		}
	} else {
		prompts.VerboseLog("config base missing, using local override")
		overrideData, err := os.ReadFile(overridePath)
		if err != nil {
			return nil, fmt.Errorf("failed to read config.local.yaml: %w", err)
		}
		mergedData = overrideData
	}
	// First pass: extract variables
	var varExtractor struct {
		Variables map[string]string `yaml:"variables"`
	}
	if err := yaml.Unmarshal(mergedData, &varExtractor); err != nil {
		return nil, fmt.Errorf("failed to parse variables: %w", err)
	}

	// Interpolate variables in the raw YAML string
	interpolatedData := interpolate(string(mergedData), varExtractor.Variables)

	var config AgentConfig
	if err := yaml.Unmarshal([]byte(interpolatedData), &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Apply defaults
	if config.Tools.Bash.DefaultTimeout == 0 {
		config.Tools.Bash.DefaultTimeout = 120000 // 2 minutes
	}
	if config.Tools.Grep.MaxResults == 0 {
		config.Tools.Grep.MaxResults = 1000
	}
	if config.Tools.Glob.MaxResults == 0 {
		config.Tools.Glob.MaxResults = 1000
	}
	if config.Tools.Read.ReadLimit == 0 {
		config.Tools.Read.ReadLimit = 80000
	}
	if config.Tools.Task.MaxConcurrent == 0 {
		config.Tools.Task.MaxConcurrent = 5
	}
	if config.Tools.WebFetch.TimeoutSecs == 0 {
		config.Tools.WebFetch.TimeoutSecs = 30
	}
	if config.Tools.WebSearch.TimeoutSecs == 0 {
		config.Tools.WebSearch.TimeoutSecs = 30
	}
	if config.Tools.WebSearch.MaxResults == 0 {
		config.Tools.WebSearch.MaxResults = 8
	}

	// Resolve API keys from environment variables if needed
	for name, model := range config.Models {
		if model.APIKeyEnv != "" && model.APIKey == "" {
			model.APIKey = os.Getenv(model.APIKeyEnv)
			config.Models[name] = model
		}
	}

	return &config, nil
}

func findConfigPath(localDir, appDir, userConfigDir, name string) string {
	if localDir != "" {
		candidate := filepath.Join(localDir, name)
		prompts.VerboseLog("config check: %q", candidate)
		if fileExists(candidate) {
			return candidate
		}
	}
	if userConfigDir != "" {
		candidate := filepath.Join(userConfigDir, name)
		prompts.VerboseLog("config check: %q", candidate)
		if fileExists(candidate) {
			return candidate
		}
	}
	if appDir != "" {
		candidate := filepath.Join(appDir, name)
		prompts.VerboseLog("config check: %q", candidate)
		if fileExists(candidate) {
			return candidate
		}
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func mergeConfigData(baseData []byte, localDir, appDir, userConfigDir string) ([]byte, error) {
	var baseMap map[string]interface{}
	if err := yaml.Unmarshal(baseData, &baseMap); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	overridePath := findConfigPath(localDir, appDir, userConfigDir, "config.local.yaml")
	if overridePath == "" {
		return baseData, nil
	}
	prompts.VerboseLog("config override found: %q", overridePath)

	overrideData, err := os.ReadFile(overridePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config.local.yaml: %w", err)
	}

	var overrideMap map[string]interface{}
	if err := yaml.Unmarshal(overrideData, &overrideMap); err != nil {
		return nil, fmt.Errorf("failed to parse config.local.yaml: %w", err)
	}

	merged := mergeMaps(baseMap, overrideMap)
	result, err := yaml.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize merged config: %w", err)
	}

	return result, nil
}

func mergeMaps(baseMap, overrideMap map[string]interface{}) map[string]interface{} {
	if baseMap == nil {
		baseMap = map[string]interface{}{}
	}
	for key, overrideValue := range overrideMap {
		if baseValue, ok := baseMap[key]; ok {
			baseValueMap, baseIsMap := baseValue.(map[string]interface{})
			overrideValueMap, overrideIsMap := overrideValue.(map[string]interface{})
			if baseIsMap && overrideIsMap {
				baseMap[key] = mergeMaps(baseValueMap, overrideValueMap)
				continue
			}
		}
		baseMap[key] = overrideValue
	}

	return baseMap
}

var varRegex = regexp.MustCompile(`\$\{([^}]+)\}`)

func interpolate(input string, variables map[string]string) string {
	return varRegex.ReplaceAllStringFunc(input, func(m string) string {
		varName := m[2 : len(m)-1]
		if strings.HasPrefix(varName, "env.") {
			envVar := strings.TrimPrefix(varName, "env.")
			return os.Getenv(envVar)
		}
		if val, ok := variables[varName]; ok {
			return val
		}
		return m // Return as is if not found
	})
}

// GetModel returns a model configuration by name
func (c *AgentConfig) GetModel(name string) (ModelConfig, error) {
	if name == "" {
		name = c.DefaultModel
	}

	model, exists := c.Models[name]
	if !exists {
		return ModelConfig{}, fmt.Errorf("model '%s' not found in configuration", name)
	}

	return model, nil
}
