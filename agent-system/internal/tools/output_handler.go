package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Size thresholds for output handling
const (
	// Threshold for writing output to file instead of keeping in memory
	largeOutputThreshold = 10 * 1024 // 10KB

	// Truncation thresholds
	mediumOutputThreshold = 20 * 1024  // 20KB
	largeOutputThreshold2 = 50 * 1024  // 50KB
	hugeOutputThreshold   = 100 * 1024 // 100KB

	// Preview sizes
	mediumPrefixSize = 4 * 1024 // 4KB
	mediumSuffixSize = 4 * 1024 // 4KB
	largePrefixSize  = 2 * 1024 // 2KB
	largeSuffixSize  = 3 * 1024 // 3KB
)

// OutputHandler manages large tool outputs by writing them to files
type OutputHandler struct {
	sessionsDir string
}

// NewOutputHandler creates a new output handler with the specified sessions directory
func NewOutputHandler(sessionsDir string) *OutputHandler {
	return &OutputHandler{
		sessionsDir: sessionsDir,
	}
}

// ProcessToolResult processes a tool result, handling large outputs by writing to files
// It modifies the result in place, replacing large content with truncated previews or file references
// The read tool is excluded from this processing as it has its own output size restrictions
func (h *OutputHandler) ProcessToolResult(result *ToolResult, toolCallID string, toolName string) {
	// Skip processing for read tool - it has its own restrictions
	if toolName == "read" {
		return
	}

	if result.Data == nil {
		return
	}

	// Get the data map
	data, ok := result.Data.(map[string]interface{})
	if !ok {
		return
	}

	// Process common output fields
	fieldsToProcess := []string{"stdout", "stderr", "content", "output", "response"}

	for _, field := range fieldsToProcess {
		if value, exists := data[field]; exists {
			if strValue, isString := value.(string); isString && len(strValue) > largeOutputThreshold {
				processed, filePath := h.processField(strValue, toolCallID, field)
				data[field] = processed
				if filePath != "" {
					data[field+"_file"] = filePath
				}
			}
		}
	}

	// Also process result.Output if it's large
	if len(result.Output) > largeOutputThreshold {
		processed, filePath := h.processField(result.Output, toolCallID, "output")
		result.Output = processed
		if filePath != "" && data != nil {
			data["output_file"] = filePath
		}
	}
}

// processField processes a single field's content, applying truncation and file writing rules
// Returns: (processedContent, filePath)
func (h *OutputHandler) processField(content, toolCallID, fieldName string) (string, string) {
	contentSize := len(content)

	// Generate unique filename using nanotime + toolCallID + fieldName
	nanoTime := time.Now().UnixNano()
	filename := fmt.Sprintf("tool-call-%d-%s-%s.txt", nanoTime, toolCallID, fieldName)

	// Create .tool-calls subdirectory
	toolCallsDir := filepath.Join(h.sessionsDir, ".tool-calls")
	if err := os.MkdirAll(toolCallsDir, 0755); err != nil {
		// If we can't create the directory, return truncated content without file reference
		return h.truncateContent(content, contentSize), ""
	}

	filePath := filepath.Join(toolCallsDir, filename)

	// Write full content to file
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		// If we can't write the file, return truncated content without file reference
		return h.truncateContent(content, contentSize), ""
	}

	// Apply truncation rules based on size
	processed := h.truncateContent(content, contentSize)

	// Add separator line with file reference
	separator := fmt.Sprintf("\n===== tool call output written to %s (%d bytes) =====\nuse tail | grep | read as you need", filePath, contentSize)

	return processed + separator, filePath
}

// truncateContent applies truncation rules based on content size
func (h *OutputHandler) truncateContent(content string, size int) string {
	// Small output (≤ 10KB) - no truncation (shouldn't reach here, but handle it)
	if size <= largeOutputThreshold {
		return content
	}

	// Medium output (10K < size ≤ 50K): first 4K + cut + last 4K
	if size <= largeOutputThreshold2 {
		if size <= mediumPrefixSize+mediumSuffixSize {
			return content
		}
		prefix := content[:mediumPrefixSize]
		suffix := content[size-mediumSuffixSize:]
		return fmt.Sprintf("%s...(cut, %d bytes total)...%s", prefix, size, suffix)
	}

	// Large output (50K < size ≤ 100K): first 2K + cut + last 3K
	if size <= hugeOutputThreshold {
		if size <= largePrefixSize+largeSuffixSize {
			return content
		}
		prefix := content[:largePrefixSize]
		suffix := content[size-largeSuffixSize:]
		return fmt.Sprintf("%s...(cut, %d bytes total)...%s", prefix, size, suffix)
	}

	// Huge output (> 100K): no preview, just empty string (separator will be added)
	return ""
}
