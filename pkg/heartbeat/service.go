// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package heartbeat

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/state"
	"github.com/sipeed/picoclaw/pkg/tools"
)

const (
	minIntervalMinutes     = 5
	defaultIntervalMinutes = 30
)

// HeartbeatHandler is the function type for handling heartbeat.
// It returns a ToolResult that can indicate async operations.
// agentID allows routing to the correct tenant context.
// channel and chatID are derived from the last active user channel for that agent.
type HeartbeatHandler func(agentID, prompt, channel, chatID string) *tools.ToolResult

// HeartbeatTarget represents a single agent's heartbeat context
type HeartbeatTarget struct {
	ID        string
	Workspace string
	State     *state.Manager
}

// HeartbeatService manages periodic heartbeat checks for multiple agents
type HeartbeatService struct {
	targets  map[string]*HeartbeatTarget
	bus      *bus.MessageBus
	handler  HeartbeatHandler
	interval time.Duration
	enabled  bool
	mu       sync.RWMutex
	stopChan chan struct{}
}

// NewHeartbeatService creates a new heartbeat service
func NewHeartbeatService(intervalMinutes int, enabled bool) *HeartbeatService {
	// Apply minimum interval
	if intervalMinutes < minIntervalMinutes && intervalMinutes != 0 {
		intervalMinutes = minIntervalMinutes
	}

	if intervalMinutes == 0 {
		intervalMinutes = defaultIntervalMinutes
	}

	return &HeartbeatService{
		targets:  make(map[string]*HeartbeatTarget),
		interval: time.Duration(intervalMinutes) * time.Minute,
		enabled:  enabled,
	}
}

// AddTarget adds a new agent workspace to be monitored by heartbeat
func (hs *HeartbeatService) AddTarget(agentID, workspace string) {
	hs.mu.Lock()
	defer hs.mu.Unlock()

	if _, exists := hs.targets[agentID]; exists {
		return
	}

	hs.targets[agentID] = &HeartbeatTarget{
		ID:        agentID,
		Workspace: workspace,
		State:     state.NewManager(workspace),
	}

	logger.InfoCF("heartbeat", "Added heartbeat target", map[string]any{
		"agent_id":  agentID,
		"workspace": workspace,
	})
}

// SetBus sets the message bus for delivering heartbeat results.
func (hs *HeartbeatService) SetBus(msgBus *bus.MessageBus) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	hs.bus = msgBus
}

// SetHandler sets the heartbeat handler.
func (hs *HeartbeatService) SetHandler(handler HeartbeatHandler) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	hs.handler = handler
}

// Start begins the heartbeat service
func (hs *HeartbeatService) Start() error {
	hs.mu.Lock()
	defer hs.mu.Unlock()

	if hs.stopChan != nil {
		logger.InfoC("heartbeat", "Heartbeat service already running")
		return nil
	}

	if !hs.enabled {
		logger.InfoC("heartbeat", "Heartbeat service disabled")
		return nil
	}

	hs.stopChan = make(chan struct{})
	go hs.runLoop(hs.stopChan)

	logger.InfoCF("heartbeat", "Heartbeat service started", map[string]any{
		"interval_minutes": hs.interval.Minutes(),
		"targets_count":    len(hs.targets),
	})

	return nil
}

// Stop gracefully stops the heartbeat service
func (hs *HeartbeatService) Stop() {
	hs.mu.Lock()
	defer hs.mu.Unlock()

	if hs.stopChan == nil {
		return
	}

	logger.InfoC("heartbeat", "Stopping heartbeat service")
	close(hs.stopChan)
	hs.stopChan = nil
}

// IsRunning returns whether the service is running
func (hs *HeartbeatService) IsRunning() bool {
	hs.mu.RLock()
	defer hs.mu.RUnlock()
	return hs.stopChan != nil
}

// runLoop runs the heartbeat ticker
func (hs *HeartbeatService) runLoop(stopChan chan struct{}) {
	ticker := time.NewTicker(hs.interval)
	defer ticker.Stop()

	// Initial delay to let agents initialize
	time.Sleep(2 * time.Second)
	hs.executeHeartbeat()

	for {
		select {
		case <-stopChan:
			return
		case <-ticker.C:
			hs.executeHeartbeat()
		}
	}
}

// executeHeartbeat performs heartbeat checks for all targets
func (hs *HeartbeatService) executeHeartbeat() {
	hs.mu.RLock()
	if !hs.enabled || hs.stopChan == nil {
		hs.mu.RUnlock()
		return
	}

	// Copy targets to process outside lock
	targets := make([]*HeartbeatTarget, 0, len(hs.targets))
	for _, t := range hs.targets {
		targets = append(targets, t)
	}
	handler := hs.handler
	hs.mu.RUnlock()

	if handler == nil {
		hs.logAllTargets("ERROR", "Heartbeat handler not configured")
		return
	}

	logger.DebugCF("heartbeat", "Executing heartbeats for all targets", map[string]any{
		"count": len(targets),
	})

	for _, target := range targets {
		hs.processTarget(target, handler)
	}
}

func (hs *HeartbeatService) processTarget(target *HeartbeatTarget, handler HeartbeatHandler) {
	prompt := hs.buildPrompt(target.Workspace)
	if prompt == "" {
		return
	}

	// Get last channel info for context
	lastChannel := target.State.GetLastChannel()
	channel, chatID := hs.parseLastChannel(target, lastChannel)

	// Debug log for channel resolution
	hs.log(target, "INFO", "Resolved channel: %s, chatID: %s (from lastChannel: %s)", channel, chatID, lastChannel)

	result := handler(target.ID, prompt, channel, chatID)

	if result == nil {
		return
	}

	// Handle different result types
	if result.IsError {
		hs.log(target, "ERROR", "Heartbeat error: %s", result.ForLLM)
		return
	}

	if result.Async {
		hs.log(target, "INFO", "Async task started: %s", result.ForLLM)
		logger.InfoCF("heartbeat", "Async heartbeat task started",
			map[string]any{
				"message": result.ForLLM,
			})
		return
	}

	// Check if silent
	if result.Silent {
		hs.log(target, "INFO", "Heartbeat OK - silent")
		return
	}

	// Send result to user
	if result.ForUser != "" {
		hs.sendResponse(target, result.ForUser)
	} else if result.ForLLM != "" {
		hs.sendResponse(target, result.ForLLM)
	}

	hs.log(target, "INFO", "Heartbeat completed: %s", result.ForLLM)
}

// buildPrompt builds the heartbeat prompt from HEARTBEAT.md in target workspace
func (hs *HeartbeatService) buildPrompt(workspace string) string {
	heartbeatPath := filepath.Join(workspace, "HEARTBEAT.md")

	data, err := os.ReadFile(heartbeatPath)
	if err != nil {
		if os.IsNotExist(err) {
			hs.createDefaultHeartbeatTemplate(workspace)
			return ""
		}
		return ""
	}

	content := string(data)
	if len(content) == 0 {
		return ""
	}

	now := time.Now().Format("2006-01-02 15:04:05")
	return fmt.Sprintf(`# Heartbeat Check

Current time: %s

You are a proactive AI assistant. This is a scheduled heartbeat check.
Review the following tasks and execute any necessary actions using available skills.
If there is nothing that requires attention, respond ONLY with: HEARTBEAT_OK

%s
`, now, content)
}

// createDefaultHeartbeatTemplate creates the default HEARTBEAT.md file
func (hs *HeartbeatService) createDefaultHeartbeatTemplate(workspace string) {
	heartbeatPath := filepath.Join(workspace, "HEARTBEAT.md")

	defaultContent := `# Heartbeat Check List

This file contains tasks for the heartbeat service to check periodically.

## Examples

- Check for unread messages
- Review upcoming calendar events
- Check device status (e.g., MaixCam)

## Instructions

- Execute ALL tasks listed below. Do NOT skip any task.
- For simple tasks (e.g., report current time), respond directly.
- For complex tasks that may take time, use the spawn tool to create a subagent.
- The spawn tool is async - subagent results will be sent to the user automatically.
- After spawning a subagent, CONTINUE to process remaining tasks.
- Only respond with HEARTBEAT_OK when ALL tasks are done AND nothing needs attention.

---

Add your heartbeat tasks below this line:
`

	if err := os.WriteFile(heartbeatPath, []byte(defaultContent), 0644); err != nil {
		logger.WarnCF("heartbeat", "Failed to create default HEARTBEAT.md", map[string]any{"error": err.Error(), "path": heartbeatPath})
	} else {
		logger.DebugCF("heartbeat", "Created default HEARTBEAT.md", map[string]any{"path": heartbeatPath})
	}
}

// sendResponse sends the heartbeat response to the last channel of the target agent
func (hs *HeartbeatService) sendResponse(target *HeartbeatTarget, response string) {
	hs.mu.RLock()
	msgBus := hs.bus
	hs.mu.RUnlock()

	if msgBus == nil {
		return
	}

	// Get last channel from state
	lastChannel := target.State.GetLastChannel()
	if lastChannel == "" {
		return
	}

	platform, userID := hs.parseLastChannel(target, lastChannel)

	// Skip internal channels that can't receive messages
	if platform == "" || userID == "" {
		return
	}

	msgBus.PublishOutbound(bus.OutboundMessage{
		Channel: platform,
		ChatID:  userID,
		Content: response,
	})

	hs.log(target, "INFO", "Heartbeat result sent to %s", platform)
}

// parseLastChannel parses the last channel string into platform and userID.
func (hs *HeartbeatService) parseLastChannel(target *HeartbeatTarget, lastChannel string) (platform, userID string) {
	if lastChannel == "" {
		return "", ""
	}

	// Parse channel format: "platform:user_id" (e.g., "telegram:123456")
	parts := strings.SplitN(lastChannel, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		hs.log(target, "ERROR", "Invalid last channel format: %s", lastChannel)
		return "", ""
	}

	platform, userID = parts[0], parts[1]

	// Skip internal channels
	if constants.IsInternalChannel(platform) {
		return "", ""
	}

	return platform, userID
}

// log writes a message to the heartbeat log file in the target's workspace
func (hs *HeartbeatService) log(target *HeartbeatTarget, level, format string, args ...any) {
	logFile := filepath.Join(target.Workspace, "heartbeat.log")
	// If logs directory exists, use it
	if _, err := os.Stat(filepath.Join(target.Workspace, "logs")); err == nil {
		logFile = filepath.Join(target.Workspace, "logs", "heartbeat.log")
	}

	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	fmt.Fprintf(f, "[%s] [%s] %s\n", timestamp, level, fmt.Sprintf(format, args...))
}

func (hs *HeartbeatService) logAllTargets(level, message string) {
	hs.mu.RLock()
	defer hs.mu.RUnlock()
	for _, target := range hs.targets {
		hs.log(target, level, "%s", message)
	}
}
