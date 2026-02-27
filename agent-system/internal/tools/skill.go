package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type SkillTool struct {
	workingDir string
}

type SkillParams struct {
	Skill string `json:"skill"`
	Args  string `json:"args,omitempty"`
}

func NewSkillTool(workingDir string) *SkillTool {
	return &SkillTool{workingDir: workingDir}
}

func (t *SkillTool) Name() string {
	return "skill"
}

func (t *SkillTool) Description() string {
	return "Execute a skill within the main conversation. When users ask you to perform tasks, check if any of the available skills match. Skills provide specialized capabilities and domain knowledge. Use this tool to load skill content by name."
}

func (t *SkillTool) Schema() *ToolSchema {
	return &ToolSchema{
		Type: "object",
		Properties: map[string]Property{
			"skill": {
				Type:        "string",
				Description: "The skill name (e.g., 'gitlab', 'jira', 'postgres')",
			},
			"args": {
				Type:        "string",
				Description: "Optional arguments for the skill",
			},
		},
		Required: []string{"skill"},
	}
}

func (t *SkillTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var args SkillParams
	if err := json.Unmarshal(params, &args); err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to parse parameters: %v", err),
		}, nil
	}

	skillName := args.Skill
	if skillName == "" {
		return ToolResult{
			Success: false,
			Error:   "skill name is required",
		}, nil
	}

	content, err := t.loadSkill(skillName)
	if err != nil {
		return ToolResult{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	return ToolResult{
		Success: true,
		Output:  content,
	}, nil
}

func (t *SkillTool) loadSkill(skillName string) (string, error) {
	searchDirs := t.getSearchDirs()

	for _, dir := range searchDirs {
		skillPath := filepath.Join(dir, skillName, "SKILL.md")
		data, err := os.ReadFile(skillPath)
		if err == nil {
			return string(data), nil
		}
	}

	return "", fmt.Errorf("skill '%s' not found in any of: %v", skillName, searchDirs)
}

func (t *SkillTool) getSearchDirs() []string {
	var dirs []string

	// 1. skills/ (project-local shorthand)
	if t.workingDir != "" {
		dirs = append(dirs, filepath.Join(t.workingDir, "skills"))
	}

	// 2. .claude/skills (project)
	if t.workingDir != "" {
		dirs = append(dirs, filepath.Join(t.workingDir, ".claude", "skills"))
	}

	// 3. ~/.claude/skills (global)
	homeDir, _ := os.UserHomeDir()
	dirs = append(dirs, filepath.Join(homeDir, ".claude", "skills"))

	return dirs
}

func (t *SkillTool) parseSkillMetadata(content string) (name, description string) {
	name = ""
	description = ""

	if !strings.HasPrefix(content, "---") {
		return
	}

	lines := strings.Split(content, "\n")
	endIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			endIdx = i
			break
		}
	}

	if endIdx < 0 {
		return
	}

	frontmatter := strings.Join(lines[1:endIdx], "\n")

	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		}
		if strings.HasPrefix(line, "description:") {
			description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		}
	}

	return
}
