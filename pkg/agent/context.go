package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/skills"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type ContextBuilder struct {
	workspace    string
	skillsLoader *skills.SkillsLoader
	memory       *MemoryStore
	tools        *tools.ToolRegistry // Direct reference to tool registry
}

func getGlobalConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".picoclaw")
}

func NewContextBuilder(workspace string) *ContextBuilder {
	// builtin skills: skills directory in current project
	// Use the skills/ directory under the current working directory
	wd, _ := os.Getwd()
	builtinSkillsDir := filepath.Join(wd, "skills")
	globalSkillsDir := filepath.Join(getGlobalConfigDir(), "skills")

	return &ContextBuilder{
		workspace:    workspace,
		skillsLoader: skills.NewSkillsLoader(workspace, globalSkillsDir, builtinSkillsDir),
		memory:       NewMemoryStore(workspace),
	}
}

// SetToolsRegistry sets the tools registry for dynamic tool summary generation.
func (cb *ContextBuilder) SetToolsRegistry(registry *tools.ToolRegistry) {
	cb.tools = registry
}

func (cb *ContextBuilder) getIdentity() string {
	now := time.Now().Format("2006-01-02 15:04 (Monday)")
	workspacePath, _ := filepath.Abs(filepath.Join(cb.workspace))
	runtime := fmt.Sprintf("%s %s, Go %s", runtime.GOOS, runtime.GOARCH, runtime.Version())

	// Build tools section dynamically
	toolsSection := cb.buildToolsSection()

	return fmt.Sprintf(`# picoclaw 🦞

You are picoclaw, a helpful AI assistant.

## Current Time
%s

## Runtime
%s

## Workspace
Your workspace is at: %s
- Memory: %s/memory/MEMORY.md
- Daily Notes: %s/memory/YYYYMM/YYYYMMDD.md
- Skills: %s/skills/{skill-name}/SKILL.md

%s

## Important Rules

1. **ALWAYS use tools** - When you need to perform an action (schedule reminders, send messages, execute commands, etc.), you MUST call the appropriate tool. Do NOT just say you'll do it or pretend to do it.

2. **Be helpful and accurate** - When using tools, briefly explain what you're doing.

3. **Memory** - When remembering something, write to %s/memory/MEMORY.md`,
		now, runtime, workspacePath, workspacePath, workspacePath, workspacePath, toolsSection, workspacePath)
}

func (cb *ContextBuilder) buildToolsSection() string {
	if cb.tools == nil {
		return ""
	}

	summaries := cb.tools.GetSummaries()
	if len(summaries) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Available Tools\n\n")
	sb.WriteString("**CRITICAL**: You MUST use tools to perform actions. Do NOT pretend to execute commands or schedule tasks.\n\n")
	sb.WriteString("You have access to the following tools:\n\n")
	for _, s := range summaries {
		sb.WriteString(s)
		sb.WriteString("\n")
	}

	return sb.String()
}

func (cb *ContextBuilder) BuildSystemPrompt() string {
	parts := []string{}

	// Core identity section
	parts = append(parts, cb.getIdentity())

	// Bootstrap files
	bootstrapContent := cb.LoadBootstrapFiles()
	if bootstrapContent != "" {
		parts = append(parts, bootstrapContent)
	}

	// Skills - show summary, AI can read full content with read_file tool
	skillsSummary := cb.skillsLoader.BuildSkillsSummary()
	if skillsSummary != "" {
		parts = append(parts, fmt.Sprintf(`# Skills

The following skills extend your capabilities. To use a skill, read its SKILL.md file using the read_file tool.

%s`, skillsSummary))
	}

	// Memory context
	memoryContext := cb.memory.GetMemoryContext()
	if memoryContext != "" {
		parts = append(parts, "# Memory\n\n"+memoryContext)
	}

	// Join with "---" separator
	return strings.Join(parts, "\n\n---\n\n")
}

func (cb *ContextBuilder) LoadBootstrapFiles() string {
	bootstrapFiles := []string{
		"AGENTS.md",
		"SOUL.md",
		"USER.md",
		"IDENTITY.md",
	}

	var result string
	for _, filename := range bootstrapFiles {
		filePath := filepath.Join(cb.workspace, filename)
		if data, err := os.ReadFile(filePath); err == nil {
			result += fmt.Sprintf("## %s\n\n%s\n\n", filename, string(data))
		}
	}

	return result
}

func (cb *ContextBuilder) BuildMessages(history []providers.Message, summary string, currentMessage string, media []string, channel, chatID, senderName string) []providers.Message {
	systemPrompt := cb.BuildSystemPrompt()

	// Add Current Session info if provided
	if channel != "" && chatID != "" {
		sessionInfo := fmt.Sprintf("\n\n## Current Session\nChannel: %s\nChat ID: %s", channel, chatID)
		if senderName != "" {
			sessionInfo += fmt.Sprintf("\nUser Nickname: %s", senderName)
		}
		systemPrompt += sessionInfo
	}

	if summary != "" {
		systemPrompt += "\n\n## Summary of Previous Conversation\n\n" + summary
	}

	// 1. Build initial sequence
	messages := make([]providers.Message, 0)
	messages = append(messages, providers.Message{
		Role:    "system",
		Content: systemPrompt,
	})

	messages = append(messages, history...)

	if currentMessage != "" {
		messages = append(messages, providers.Message{
			Role:    "user",
			Content: currentMessage,
		})
	}

	// 2. Protocol Fix: Use a robust healer to ensure the sequence is valid for any API
	return cb.HealProtocol(messages)
}

// HealProtocol ensures the message sequence follows strict LLM API rules:
// 1. First message must be 'system' or 'user' (never 'tool').
// 2. Messages with role 'tool' must follow an 'assistant' message with 'tool_calls'.
// 3. 'assistant' messages with 'tool_calls' must be followed by their corresponding 'tool' messages.
// 4. No non-tool message can interrupt an assistant-tool chain.
func (cb *ContextBuilder) HealProtocol(messages []providers.Message) []providers.Message {
	if len(messages) == 0 {
		return messages
	}

	healed := make([]providers.Message, 0, len(messages))
	var lastAssistantWithTools *providers.Message
	pendingToolIDs := make(map[string]bool)

	for i := 0; i < len(messages); i++ {
		m := messages[i]

		if m.Role == "tool" {
			// Rule: Tool message must have a preceding assistant call
			if !pendingToolIDs[m.ToolCallID] {
				logger.WarnCF("agent", "HealProtocol: Removing orphan tool message", map[string]interface{}{"id": m.ToolCallID})
				continue
			}
			delete(pendingToolIDs, m.ToolCallID)
			healed = append(healed, m)
			continue
		}

		// Non-tool message encountered.
		// If we had an assistant calling tools, and we reached a user/system message before all tools responded,
		// we MUST clean up that assistant message's tool_calls to avoid 400 errors.
		if len(pendingToolIDs) > 0 && lastAssistantWithTools != nil {
			logger.WarnCF("agent", "HealProtocol: Breaking tool chain due to intervening message", map[string]interface{}{
				"role":           m.Role,
				"orphaned_count": len(pendingToolIDs),
			})
			lastAssistantWithTools.ToolCalls = nil
			pendingToolIDs = make(map[string]bool)
		}

		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			lastAssistantWithTools = &m
			for _, tc := range m.ToolCalls {
				pendingToolIDs[tc.ID] = true
			}
			healed = append(healed, m)
			// Small hack: if we modified the pointer m, we need to ensure the refreshed lastAssistantWithTools
			// is the one in the 'healed' slice.
			lastAssistantWithTools = &healed[len(healed)-1]
		} else {
			// Normal user/system message
			healed = append(healed, m)
			lastAssistantWithTools = nil
		}
	}

	// Final check: if the last message is an assistant calling tools with no responses,
	// and there are no more messages coming (EOF), we must strip tool_calls to allow the LLM to just "talk".
	if len(pendingToolIDs) > 0 && lastAssistantWithTools != nil {
		logger.WarnCF("agent", "HealProtocol: Stripping terminal tool calls with no responses", nil)
		lastAssistantWithTools.ToolCalls = nil
	}

	// DeepScan Rule: First non-system message should not be 'tool'
	// Since we already filtered out orphan tools in the loop, healed[0] is system.
	// But if healed[1] is 'tool' for some reason, we'd still be in trouble.
	// However, our loop logic ensures tool messages ONLY follow assistant calls.

	return healed
}

func (cb *ContextBuilder) AddToolResult(messages []providers.Message, toolCallID, toolName, result string) []providers.Message {
	messages = append(messages, providers.Message{
		Role:       "tool",
		Content:    result,
		ToolCallID: toolCallID,
	})
	return messages
}

func (cb *ContextBuilder) AddAssistantMessage(messages []providers.Message, content string, toolCalls []map[string]interface{}) []providers.Message {
	msg := providers.Message{
		Role:    "assistant",
		Content: content,
	}
	// Always add assistant message, whether or not it has tool calls
	messages = append(messages, msg)
	return messages
}

func (cb *ContextBuilder) loadSkills() string {
	allSkills := cb.skillsLoader.ListSkills()
	if len(allSkills) == 0 {
		return ""
	}

	var skillNames []string
	for _, s := range allSkills {
		skillNames = append(skillNames, s.Name)
	}

	content := cb.skillsLoader.LoadSkillsForContext(skillNames)
	if content == "" {
		return ""
	}

	return "# Skill Definitions\n\n" + content
}

// GetSkillsInfo returns information about loaded skills.
func (cb *ContextBuilder) GetSkillsInfo() map[string]interface{} {
	allSkills := cb.skillsLoader.ListSkills()
	skillNames := make([]string, 0, len(allSkills))
	for _, s := range allSkills {
		skillNames = append(skillNames, s.Name)
	}
	return map[string]interface{}{
		"total":     len(allSkills),
		"available": len(allSkills),
		"names":     skillNames,
	}
}
