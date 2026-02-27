package agent

import (
	"context"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

// Test that cron sessions don't trigger auto-registration
func TestProcessDirectWithChannel_CronNoAutoRegister(t *testing.T) {
	cfg := testCfg(nil)
	cfg.Agents.Defaults.AutoRegister = &config.AutoRegisterConfig{
		Enabled: true,
		Pattern: "user-{channel}-{peer_id}",
	}

	provider := &mockRegistryProvider{}
	msgBus := bus.NewMessageBus()
	loop := NewAgentLoop(cfg, msgBus, provider)

	// Simulate a cron job execution
	sessionKey := "cron-test-job-123"
	_, err := loop.ProcessDirectWithChannel(
		context.Background(),
		"test message",
		sessionKey,
		"cli",
		"direct",
	)

	if err != nil {
		t.Fatalf("ProcessDirectWithChannel failed: %v", err)
	}
	// Verify that no auto-registered agent was created
	// The agent ID would be "user-cli-test-job-123" if auto-register was triggered
	autoAgentID := "user-cli-test-job-123"
	if loop.registry.HasAgent(autoAgentID) {
		t.Errorf("Cron session should NOT trigger auto-registration, but found agent: %s", autoAgentID)
	}

	// Verify only the default agent exists
	ids := loop.registry.ListAgentIDs()
	if len(ids) != 1 || ids[0] != "main" {
		t.Errorf("Expected only 'main' agent, got: %v", ids)
	}
}

// Test that heartbeat sessions don't trigger auto-registration
func TestProcessDirectWithChannel_HeartbeatNoAutoRegister(t *testing.T) {
	cfg := testCfg(nil)
	cfg.Agents.Defaults.AutoRegister = &config.AutoRegisterConfig{
		Enabled: true,
		Pattern: "user-{channel}-{peer_id}",
	}
	provider := &mockRegistryProvider{}
	msgBus := bus.NewMessageBus()
	loop := NewAgentLoop(cfg, msgBus, provider)

	// Simulate a heartbeat execution
	sessionKey := "heartbeat-test"
	_, err := loop.ProcessDirectWithChannel(
		context.Background(),
		"test message",
		sessionKey,
		"cli",
		"direct",
	)

	if err != nil {
		t.Fatalf("ProcessDirectWithChannel failed: %v", err)
	}
	// Verify that no auto-registered agent was created
	autoAgentID := "user-cli-test"
	if loop.registry.HasAgent(autoAgentID) {
		t.Errorf("Heartbeat session should NOT trigger auto-registration, but found agent: %s", autoAgentID)
	}
}

// Test that agent-scoped sessions don't trigger auto-registration
func TestProcessDirectWithChannel_AgentScopedNoAutoRegister(t *testing.T) {
	cfg := testCfg(nil)
	cfg.Agents.Defaults.AutoRegister = &config.AutoRegisterConfig{
		Enabled: true,
		Pattern: "user-{channel}-{peer_id}",
	}
	provider := &mockRegistryProvider{}
	msgBus := bus.NewMessageBus()
	loop := NewAgentLoop(cfg, msgBus, provider)

	// Simulate an agent-scoped session
	sessionKey := "agent:main:custom-session"
	_, err := loop.ProcessDirectWithChannel(
		context.Background(),
		"test message",
		sessionKey,
		"cli",
		"direct",
	)

	if err != nil {
		t.Fatalf("ProcessDirectWithChannel failed: %v", err)
	}
	// Verify that no auto-registered agent was created
	autoAgentID := "user-cli-main"
	if loop.registry.HasAgent(autoAgentID) {
		t.Errorf("Agent-scoped session should NOT trigger auto-registration, but found agent: %s", autoAgentID)
	}
}

// Test that regular CLI sessions DO trigger auto-registration
func TestProcessDirectWithChannel_RegularCLIAutoRegister(t *testing.T) {
	cfg := testCfg(nil)
	cfg.Agents.Defaults.AutoRegister = &config.AutoRegisterConfig{
		Enabled:           true,
		Pattern:           "user-{channel}-{peer_id}",
		WorkspaceTemplate: t.TempDir() + "/tenants/{agent_id}",
	}
	provider := &mockRegistryProvider{}
	msgBus := bus.NewMessageBus()
	loop := NewAgentLoop(cfg, msgBus, provider)

	// Simulate a regular CLI user session
	sessionKey := "cli:alice"
	_, err := loop.ProcessDirectWithChannel(
		context.Background(),
		"test message",
		sessionKey,
		"cli",
		"direct",
	)

	if err != nil {
		t.Fatalf("ProcessDirectWithChannel failed: %v", err)
	}
	// Verify that auto-registered agent WAS created
	autoAgentID := "user-cli-alice"
	if !loop.registry.HasAgent(autoAgentID) {
		t.Errorf("Regular CLI session should trigger auto-registration, but agent not found: %s", autoAgentID)
	}

	// Verify two agents exist: main and auto-registered
	ids := loop.registry.ListAgentIDs()
	if len(ids) != 2 {
		t.Errorf("Expected 2 agents (main + auto-registered), got: %v", ids)
	}
}
