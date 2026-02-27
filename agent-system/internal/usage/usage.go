package usage

import "strings"

// Usage represents token usage
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

func getInt(m map[string]interface{}, key string) (int, bool) {
	val, ok := m[key]
	if !ok {
		return 0, false
	}
	switch v := val.(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	case int64:
		return int(v), true
	}
	return 0, false
}

func ExtractUsage(info map[string]interface{}) Usage {
	var u Usage
	if info == nil {
		return u
	}

	// Try common keys at top level
	if val, ok := getInt(info, "input_tokens"); ok {
		u.InputTokens = val
	} else if val, ok := getInt(info, "PromptTokens"); ok {
		u.InputTokens = val
	} else if val, ok := getInt(info, "InputTokens"); ok {
		u.InputTokens = val
	}

	if val, ok := getInt(info, "output_tokens"); ok {
		u.OutputTokens = val
	} else if val, ok := getInt(info, "CompletionTokens"); ok {
		u.OutputTokens = val
	} else if val, ok := getInt(info, "OutputTokens"); ok {
		u.OutputTokens = val
	}

	// Check for a nested "usage" map
	if usageMap, ok := info["usage"].(map[string]interface{}); ok {
		if val, ok := getInt(usageMap, "input_tokens"); ok {
			u.InputTokens = val
		}
		if val, ok := getInt(usageMap, "output_tokens"); ok {
			u.OutputTokens = val
		}
		if val, ok := getInt(usageMap, "cache_read_input_tokens"); ok {
			u.CacheReadTokens = val
		}
		if val, ok := getInt(usageMap, "cache_creation_input_tokens"); ok {
			u.CacheWriteTokens = val
		}
		if val, ok := getInt(usageMap, "cache_read_tokens"); ok {
			u.CacheReadTokens = val
		}
		if val, ok := getInt(usageMap, "cache_write_tokens"); ok {
			u.CacheWriteTokens = val
		}
		if val, ok := getInt(usageMap, "PromptCachedTokens"); ok {
			u.CacheReadTokens = val
		}
		if val, ok := getInt(usageMap, "ThinkingCachedTokens"); ok {
			u.CacheReadTokens = val
		}
	}

	// Bedrock specific metrics
	if metrics, ok := info["amazon-bedrock-invocationMetrics"].(map[string]interface{}); ok {
		if val, ok := getInt(metrics, "inputTokenCount"); ok {
			u.InputTokens = val
		}
		if val, ok := getInt(metrics, "outputTokenCount"); ok {
			u.OutputTokens = val
		}
	}

	// Anthropic/Bedrock caching keys at top level
	if val, ok := getInt(info, "CacheReadInputTokens"); ok {
		u.CacheReadTokens = val
	} else if val, ok := getInt(info, "cache_read_input_tokens"); ok {
		u.CacheReadTokens = val
	} else if val, ok := getInt(info, "cache_read_tokens"); ok {
		u.CacheReadTokens = val
	} else if val, ok := getInt(info, "input_tokens_cache_read"); ok {
		u.CacheReadTokens = val
	} else if val, ok := getInt(info, "PromptCachedTokens"); ok {
		u.CacheReadTokens = val
	} else if val, ok := getInt(info, "ThinkingCachedTokens"); ok {
		u.CacheReadTokens = val
	}

	if val, ok := getInt(info, "CacheCreationInputTokens"); ok {
		u.CacheWriteTokens = val
	} else if val, ok := getInt(info, "cache_creation_input_tokens"); ok {
		u.CacheWriteTokens = val
	} else if val, ok := getInt(info, "cache_write_tokens"); ok {
		u.CacheWriteTokens = val
	} else if val, ok := getInt(info, "input_tokens_cache_creation"); ok {
		u.CacheWriteTokens = val
	} else if val, ok := getInt(info, "CacheWriteInputTokens"); ok {
		u.CacheWriteTokens = val
	}

	return u
}

func TrimSuffix(s, suffix string) string {
	return strings.TrimSuffix(s, suffix)
}
