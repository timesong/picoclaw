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

3. **Memory** - When interacting with me if something seems memorable, update %s/memory/MEMORY.md`,
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
	sb.WriteString(
		"**CRITICAL**: You MUST use tools to perform actions. Do NOT pretend to execute commands or schedule tasks.\n\n",
	)
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

	var sb strings.Builder
	for _, filename := range bootstrapFiles {
		filePath := filepath.Join(cb.workspace, filename)
		if data, err := os.ReadFile(filePath); err == nil {
			fmt.Fprintf(&sb, "## %s\n\n%s\n\n", filename, data)
		}
	}

	return sb.String()
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

	// Log system prompt summary for debugging (debug mode only)
	logger.DebugCF("agent", "System prompt built",
		map[string]any{
			"total_chars":   len(systemPrompt),
			"total_lines":   strings.Count(systemPrompt, "\n") + 1,
			"section_count": strings.Count(systemPrompt, "\n\n---\n\n") + 1,
		})

	// Log preview of system prompt (avoid logging huge content)
	preview := systemPrompt
	if len(preview) > 500 {
		preview = preview[:500] + "... (truncated)"
	}
	logger.DebugCF("agent", "System prompt preview",
		map[string]any{
			"preview": preview,
		})

	if summary != "" {
		systemPrompt += "\n\n## Summary of Previous Conversation\n\n" + summary
	}

	// Debug: Log history before sanitization
	logger.DebugCF("agent", "BuildMessages: Original history length", map[string]interface{}{"count": len(history)})
	
	history = sanitizeHistoryForProvider(history)
	
	// Debug: Log history after sanitization
	logger.DebugCF("agent", "BuildMessages: Sanitized history length", map[string]interface{}{"count": len(history)})

	messages := make([]providers.Message, 0)
	messages = append(messages, providers.Message{
		Role:    "system",
		Content: systemPrompt,
	})

	messages = append(messages, history...)

	if strings.TrimSpace(currentMessage) != "" {
		messages = append(messages, providers.Message{
			Role:    "user",
			Content: currentMessage,
		})
	}

	// Debug: Log messages before healing
	logger.DebugCF("agent", "BuildMessages: Before HealProtocol", map[string]interface{}{"count": len(messages)})
	for i, msg := range messages {
		logger.DebugCF("agent", fmt.Sprintf("  [%d] %s", i, msg.Role), map[string]interface{}{
			"has_tool_calls": len(msg.ToolCalls) > 0,
			"tool_call_id":   msg.ToolCallID,
		})
	}

	// 2. Protocol Fix: Use a robust healer to ensure the sequence is valid for any API
	healed := cb.HealProtocol(messages)
	
	// Debug: Log messages after healing
	logger.DebugCF("agent", "BuildMessages: After HealProtocol", map[string]interface{}{"count": len(healed)})
	for i, msg := range healed {
		logger.DebugCF("agent", fmt.Sprintf("  [%d] %s", i, msg.Role), map[string]interface{}{
			"has_tool_calls": len(msg.ToolCalls) > 0,
			"tool_call_id":   msg.ToolCallID,
		})
	}
	
	return healed
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
	lastAssistantWithToolsIdx := -1 // Use index instead of pointer to avoid stale references
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
		if len(pendingToolIDs) > 0 && lastAssistantWithToolsIdx >= 0 {
			logger.WarnCF("agent", "HealProtocol: Breaking tool chain due to intervening message", map[string]interface{}{
				"role":           m.Role,
				"orphaned_count": len(pendingToolIDs),
			})
			healed[lastAssistantWithToolsIdx].ToolCalls = nil
			pendingToolIDs = make(map[string]bool)
			lastAssistantWithToolsIdx = -1
		}

		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				pendingToolIDs[tc.ID] = true
			}
			healed = append(healed, m)
			lastAssistantWithToolsIdx = len(healed) - 1
		} else {
			// Normal user/system message
			healed = append(healed, m)
			lastAssistantWithToolsIdx = -1
		}
	}

	// Final check: if the last message is an assistant calling tools with no responses,
	// and there are no more messages coming (EOF), we must strip tool_calls to allow the LLM to just "talk".
	if len(pendingToolIDs) > 0 && lastAssistantWithToolsIdx >= 0 {
		logger.WarnCF("agent", "HealProtocol: Stripping terminal tool calls with no responses", nil)
		healed[lastAssistantWithToolsIdx].ToolCalls = nil
	}

	// DeepScan Rule: First non-system message should not be 'tool'
	// Since we already filtered out orphan tools in the loop, healed[0] is system.
	// But if healed[1] is 'tool' for some reason, we'd still be in trouble.
	// However, our loop logic ensures tool messages ONLY follow assistant calls.

	return healed
}

func sanitizeHistoryForProvider(history []providers.Message) []providers.Message {
	if len(history) == 0 {
		return history
	}

	sanitized := make([]providers.Message, 0, len(history))
	pendingToolCallIDs := make(map[string]bool)
	lastAssistantIdx := -1

	for i, msg := range history {
		switch msg.Role {
		case "tool":
			// A tool message must have a corresponding tool call from a previous assistant
			if !pendingToolCallIDs[msg.ToolCallID] {
				logger.DebugCF("agent", "sanitizeHistory: Dropping orphaned tool message", map[string]interface{}{
					"index":        i,
					"tool_call_id": msg.ToolCallID,
				})
				continue
			}
			delete(pendingToolCallIDs, msg.ToolCallID)
			sanitized = append(sanitized, msg)

		case "assistant":
			// If previous assistant had unfulfilled tool calls, we need to drop it
			if len(pendingToolCallIDs) > 0 && lastAssistantIdx >= 0 {
				logger.WarnCF("agent", "sanitizeHistory: Removing assistant with unfulfilled tool calls", map[string]interface{}{
					"index":        lastAssistantIdx,
					"orphaned_ids": len(pendingToolCallIDs),
				})
				// Remove the problematic assistant message
				sanitized = removeMessageAt(sanitized, lastAssistantIdx)
				pendingToolCallIDs = make(map[string]bool)
				lastAssistantIdx = -1
			}

			if len(msg.ToolCalls) > 0 {
				// Record this assistant's tool calls
				for _, tc := range msg.ToolCalls {
					pendingToolCallIDs[tc.ID] = true
				}
				sanitized = append(sanitized, msg)
				lastAssistantIdx = len(sanitized) - 1
			} else {
				sanitized = append(sanitized, msg)
				lastAssistantIdx = -1
			}

		case "user", "system":
			// If we encounter a user/system message with pending tool calls, drop the assistant
			if len(pendingToolCallIDs) > 0 && lastAssistantIdx >= 0 {
				logger.WarnCF("agent", "sanitizeHistory: Removing assistant with unfulfilled tool calls before user message", map[string]interface{}{
					"assistant_idx": lastAssistantIdx,
					"orphaned_ids":  len(pendingToolCallIDs),
				})
				sanitized = removeMessageAt(sanitized, lastAssistantIdx)
				pendingToolCallIDs = make(map[string]bool)
				lastAssistantIdx = -1
			}
			sanitized = append(sanitized, msg)

		default:
			sanitized = append(sanitized, msg)
		}
	}

	// Final cleanup: if we end with unfulfilled tool calls, remove that assistant
	if len(pendingToolCallIDs) > 0 && lastAssistantIdx >= 0 {
		logger.WarnCF("agent", "sanitizeHistory: Removing final assistant with unfulfilled tool calls", map[string]interface{}{
			"orphaned_ids": len(pendingToolCallIDs),
		})
		sanitized = removeMessageAt(sanitized, lastAssistantIdx)
	}

	return sanitized
}

// removeMessageAt removes the message at the given index from the slice
func removeMessageAt(messages []providers.Message, index int) []providers.Message {
	if index < 0 || index >= len(messages) {
		return messages
	}
	return append(messages[:index], messages[index+1:]...)
}

func (cb *ContextBuilder) AddToolResult(
	messages []providers.Message,
	toolCallID, toolName, result string,
) []providers.Message {
	messages = append(messages, providers.Message{
		Role:       "tool",
		Content:    result,
		ToolCallID: toolCallID,
	})
	return messages
}

func (cb *ContextBuilder) AddAssistantMessage(
	messages []providers.Message,
	content string,
	toolCalls []map[string]any,
) []providers.Message {
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
func (cb *ContextBuilder) GetSkillsInfo() map[string]any {
	allSkills := cb.skillsLoader.ListSkills()
	skillNames := make([]string, 0, len(allSkills))
	for _, s := range allSkills {
		skillNames = append(skillNames, s.Name)
	}
	return map[string]any{
		"total":     len(allSkills),
		"available": len(allSkills),
		"names":     skillNames,
	}
}
