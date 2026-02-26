package tools

import (
	"context"
	"fmt"
	"strings"
)

type SpawnTool struct {
	manager        *SubagentManager
	originChannel  string
	originChatID   string
	allowlistCheck func(targetAgentID string) bool
	callback       AsyncCallback // For async completion notification
}

func NewSpawnTool(manager *SubagentManager) *SpawnTool {
	return &SpawnTool{
		manager:       manager,
		originChannel: "cli",
		originChatID:  "direct",
	}
}

// SetCallback implements AsyncTool interface for async completion notification
func (t *SpawnTool) SetCallback(cb AsyncCallback) {
	t.callback = cb
}

func (t *SpawnTool) Name() string {
	return "spawn"
}

func (t *SpawnTool) Description() string {
	return "Spawn a subagent to handle a task in the background. Use this for complex or time-consuming tasks that can run independently. The subagent will complete the task and report back when done. IMPORTANT: DO NOT use this tool for scheduling, reminders, waiting or delaying tasks. For any time-based tasks or reminders, you MUST use the 'cron' tool instead."
}

func (t *SpawnTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{
				"type":        "string",
				"description": "The task for subagent to complete",
			},
			"label": map[string]any{
				"type":        "string",
				"description": "Optional short label for the task (for display)",
			},
			"agent_id": map[string]any{
				"type":        "string",
				"description": "Optional target agent ID to delegate the task to",
			},
		},
		"required": []string{"task"},
	}
}

func (t *SpawnTool) SetContext(channel, chatID string) {
	t.originChannel = channel
	t.originChatID = chatID
}

func (t *SpawnTool) SetAllowlistChecker(check func(targetAgentID string) bool) {
	t.allowlistCheck = check
}

func (t *SpawnTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	task, ok := args["task"].(string)
	if !ok || strings.TrimSpace(task) == "" {
		return ErrorResult("task is required and must be a non-empty string")
	}

	label, _ := args["label"].(string)
	agentID, _ := args["agent_id"].(string)

	// Detect time-based tasks that should use cron tool instead
	timeKeywords := []string{
		"等待", "wait", "delay", "延迟", "sleep",
		"分钟后", "小时后", "秒后", "天后",
		"after", "later", "in ", "minutes", "hours", "seconds", "days",
		"提醒", "remind", "alert", "notify", "通知",
		"定时", "schedule", "scheduled", "timer",
	}
	taskLower := strings.ToLower(task)
	for _, keyword := range timeKeywords {
		if strings.Contains(taskLower, strings.ToLower(keyword)) {
			return ErrorResult("This task appears to be time-based or scheduling related. Please use the 'cron' tool instead of 'spawn' for reminders, delays, or scheduled tasks.")
		}
	}

	// Check allowlist if targeting a specific agent
	if agentID != "" && t.allowlistCheck != nil {
		if !t.allowlistCheck(agentID) {
			return ErrorResult(fmt.Sprintf("not allowed to spawn agent '%s'", agentID))
		}
	}

	if t.manager == nil {
		return ErrorResult("Subagent manager not configured")
	}

	// Pass callback to manager for async completion notification
	result, err := t.manager.Spawn(ctx, task, label, agentID, t.originChannel, t.originChatID, t.callback)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to spawn subagent: %v", err))
	}

	// Return AsyncResult since the task runs in background
	return AsyncResult(result)
}

