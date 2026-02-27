package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type mockRegistryProvider struct{}

func (m *mockRegistryProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "mock", FinishReason: "stop"}, nil
}

func (m *mockRegistryProvider) GetDefaultModel() string {
	return "mock-model"
}

func testCfg(agents []config.AgentConfig) *config.Config {
	return &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         "/tmp/picoclaw-test-registry",
				Model:             "gpt-4",
				MaxTokens:         8192,
				MaxToolIterations: 10,
			},
			List: agents,
		},
	}
}

func TestNewAgentRegistry_ImplicitMain(t *testing.T) {
	cfg := testCfg(nil)
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	ids := registry.ListAgentIDs()
	if len(ids) != 1 || ids[0] != "main" {
		t.Errorf("expected implicit main agent, got %v", ids)
	}

	agent, ok := registry.GetAgent("main")
	if !ok || agent == nil {
		t.Fatal("expected to find 'main' agent")
	}
	if agent.ID != "main" {
		t.Errorf("agent.ID = %q, want 'main'", agent.ID)
	}
}

func TestNewAgentRegistry_ExplicitAgents(t *testing.T) {
	cfg := testCfg([]config.AgentConfig{
		{ID: "sales", Default: true, Name: "Sales Bot"},
		{ID: "support", Name: "Support Bot"},
	})
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	ids := registry.ListAgentIDs()
	if len(ids) != 2 {
		t.Fatalf("expected 2 agents, got %d: %v", len(ids), ids)
	}

	sales, ok := registry.GetAgent("sales")
	if !ok || sales == nil {
		t.Fatal("expected to find 'sales' agent")
	}
	if sales.Name != "Sales Bot" {
		t.Errorf("sales.Name = %q, want 'Sales Bot'", sales.Name)
	}

	support, ok := registry.GetAgent("support")
	if !ok || support == nil {
		t.Fatal("expected to find 'support' agent")
	}
}

func TestAgentRegistry_GetAgent_Normalize(t *testing.T) {
	cfg := testCfg([]config.AgentConfig{
		{ID: "my-agent", Default: true},
	})
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	agent, ok := registry.GetAgent("My-Agent")
	if !ok || agent == nil {
		t.Fatal("expected to find agent with normalized ID")
	}
	if agent.ID != "my-agent" {
		t.Errorf("agent.ID = %q, want 'my-agent'", agent.ID)
	}
}

func TestAgentRegistry_GetDefaultAgent(t *testing.T) {
	cfg := testCfg([]config.AgentConfig{
		{ID: "alpha"},
		{ID: "beta", Default: true},
	})
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	// GetDefaultAgent first checks for "main", then returns any
	agent := registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected a default agent")
	}
}

func TestAgentRegistry_CanSpawnSubagent(t *testing.T) {
	cfg := testCfg([]config.AgentConfig{
		{
			ID:      "parent",
			Default: true,
			Subagents: &config.SubagentsConfig{
				AllowAgents: []string{"child1", "child2"},
			},
		},
		{ID: "child1"},
		{ID: "child2"},
		{ID: "restricted"},
	})
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	if !registry.CanSpawnSubagent("parent", "child1") {
		t.Error("expected parent to be allowed to spawn child1")
	}
	if !registry.CanSpawnSubagent("parent", "child2") {
		t.Error("expected parent to be allowed to spawn child2")
	}
	if registry.CanSpawnSubagent("parent", "restricted") {
		t.Error("expected parent to NOT be allowed to spawn restricted")
	}
	if registry.CanSpawnSubagent("child1", "child2") {
		t.Error("expected child1 to NOT be allowed to spawn (no subagents config)")
	}
}

func TestAgentRegistry_CanSpawnSubagent_Wildcard(t *testing.T) {
	cfg := testCfg([]config.AgentConfig{
		{
			ID:      "admin",
			Default: true,
			Subagents: &config.SubagentsConfig{
				AllowAgents: []string{"*"},
			},
		},
		{ID: "any-agent"},
	})
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	if !registry.CanSpawnSubagent("admin", "any-agent") {
		t.Error("expected wildcard to allow spawning any agent")
	}
	if !registry.CanSpawnSubagent("admin", "nonexistent") {
		t.Error("expected wildcard to allow spawning even nonexistent agents")
	}
}

func TestAgentInstance_Model(t *testing.T) {
	model := &config.AgentModelConfig{Primary: "claude-opus"}
	cfg := testCfg([]config.AgentConfig{
		{ID: "custom", Default: true, Model: model},
	})
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	agent, _ := registry.GetAgent("custom")
	if agent.Model != "claude-opus" {
		t.Errorf("agent.Model = %q, want 'claude-opus'", agent.Model)
	}
}

func TestAgentInstance_FallbackInheritance(t *testing.T) {
	cfg := testCfg([]config.AgentConfig{
		{ID: "inherit", Default: true},
	})
	cfg.Agents.Defaults.ModelFallbacks = []string{"openai/gpt-4o-mini", "anthropic/haiku"}
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	agent, _ := registry.GetAgent("inherit")
	if len(agent.Fallbacks) != 2 {
		t.Errorf("expected 2 fallbacks inherited from defaults, got %d", len(agent.Fallbacks))
	}
}

func TestAgentInstance_FallbackExplicitEmpty(t *testing.T) {
	model := &config.AgentModelConfig{
		Primary:   "gpt-4",
		Fallbacks: []string{}, // explicitly empty = disable
	}
	cfg := testCfg([]config.AgentConfig{
		{ID: "no-fallback", Default: true, Model: model},
	})
	cfg.Agents.Defaults.ModelFallbacks = []string{"should-not-inherit"}
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	agent, _ := registry.GetAgent("no-fallback")
	if len(agent.Fallbacks) != 0 {
		t.Errorf("expected 0 fallbacks (explicit empty), got %d: %v", len(agent.Fallbacks), agent.Fallbacks)
	}
}

// Auto-registration tests

func TestAgentRegistry_ShouldAutoRegister_Disabled(t *testing.T) {
	cfg := testCfg(nil)
	// Auto-register is disabled by default
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	if registry.shouldAutoRegister("user-telegram-123") {
		t.Error("expected shouldAutoRegister to return false when disabled")
	}
}

func TestAgentRegistry_ShouldAutoRegister_Enabled(t *testing.T) {
	cfg := testCfg(nil)
	cfg.Agents.Defaults.AutoRegister = &config.AutoRegisterConfig{
		Enabled: true,
		Pattern: "user-{channel}-{peer_id}",
	}
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})
	if !registry.shouldAutoRegister("user-telegram-123") {
		t.Error("expected shouldAutoRegister to return true for valid pattern")
	}
}

func TestAgentRegistry_ShouldAutoRegister_PatternMismatch(t *testing.T) {
	cfg := testCfg(nil)
	cfg.Agents.Defaults.AutoRegister = &config.AutoRegisterConfig{
		Enabled: true,
		Pattern: "user-{channel}-{peer_id}",
	}
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	// Pattern expects dashes, but agent ID has no dashes
	if registry.shouldAutoRegister("invalidagentid") {
		t.Error("expected shouldAutoRegister to return false for pattern mismatch")
	}
}

func TestAgentRegistry_AutoRegisterAgent_Success(t *testing.T) {
	cfg := testCfg(nil)
	cfg.Agents.Defaults.AutoRegister = &config.AutoRegisterConfig{
		Enabled:           true,
		Pattern:           "user-{peer_id}",
		WorkspaceTemplate: t.TempDir() + "/tenants/{agent_id}",
	}
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	agentID := "user-test-123"
	agent, ok := registry.autoRegisterAgent(agentID)

	if !ok || agent == nil {
		t.Fatal("expected autoRegisterAgent to succeed")
	}
	if agent.ID != agentID {
		t.Errorf("agent.ID = %q, want %q", agent.ID, agentID)
	}

	// Verify agent is registered
	retrieved, ok := registry.GetAgent(agentID)
	if !ok || retrieved == nil {
		t.Error("expected auto-registered agent to be retrievable")
	}
}

func TestAgentRegistry_GetAgent_AutoRegister(t *testing.T) {
	cfg := testCfg(nil)
	cfg.Agents.Defaults.AutoRegister = &config.AutoRegisterConfig{
		Enabled:           true,
		Pattern:           "user-{peer_id}",
		WorkspaceTemplate: t.TempDir() + "/tenants/{agent_id}",
	}
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	agentID := "user-auto-789"

	// First call should auto-register
	agent, ok := registry.GetAgent(agentID)
	if !ok || agent == nil {
		t.Fatal("expected GetAgent to auto-register agent")
	}
	if agent.ID != agentID {
		t.Errorf("agent.ID = %q, want %q", agent.ID, agentID)
	}
	// Second call should return same agent
	agent2, ok2 := registry.GetAgent(agentID)
	if !ok2 || agent2 == nil {
		t.Fatal("expected GetAgent to return existing agent")
	}
	if agent != agent2 {
		t.Error("expected same agent instance on subsequent calls")
	}
}

func TestAgentRegistry_BuildWorkspacePath(t *testing.T) {
	tests := []struct {
		name     string
		template string
		agentID  string
		want     string
	}{
		{
			name:     "simple template",
			template: "/tmp/tenants/{agent_id}",
			agentID:  "user-123",
			want:     "/tmp/tenants/user-123",
		},
		{
			name:     "nested template",
			template: "/var/picoclaw/{agent_id}/workspace",
			agentID:  "tenant-abc",
			want:     "/var/picoclaw/tenant-abc/workspace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testCfg(nil)
			cfg.Agents.Defaults.AutoRegister = &config.AutoRegisterConfig{
				WorkspaceTemplate: tt.template,
			}
			registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

			got := registry.buildWorkspacePath(tt.agentID)
			if got != tt.want {
				t.Errorf("buildWorkspacePath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAgentRegistry_CopyFileIfNotExists(t *testing.T) {
	cfg := testCfg(nil)
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	srcDir := t.TempDir()
	dstDir := t.TempDir()

	srcFile := filepath.Join(srcDir, "test.txt")
	dstFile := filepath.Join(dstDir, "test.txt")

	// Create source file
	if err := os.WriteFile(srcFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("failed to create source file: %v", err)
	}

	// First copy should succeed
	err := registry.copyFileIfNotExists(srcFile, dstFile)
	if err != nil {
		t.Errorf("copyFileIfNotExists failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(dstFile); os.IsNotExist(err) {
		t.Error("expected destination file to exist")
	}

	// Second copy should be skipped (no error)
	err = registry.copyFileIfNotExists(srcFile, dstFile)
	if err != nil {
		t.Errorf("copyFileIfNotExists should skip existing file: %v", err)
	}
}

func TestAgentRegistry_InitializeTenantWorkspace_WithInheritance(t *testing.T) {
	// Create a temporary default workspace with test files
	defaultWorkspace := t.TempDir()
	testFile := filepath.Join(defaultWorkspace, "SOUL.md")
	if err := os.WriteFile(testFile, []byte("# Default Soul"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	cfg := testCfg(nil)
	cfg.Agents.Defaults.Workspace = defaultWorkspace
	cfg.Agents.Defaults.AutoRegister = &config.AutoRegisterConfig{
		Enabled:            true,
		InheritFromDefault: true,
		CopyFiles:          []string{"SOUL.md"},
	}
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	workspacePath := filepath.Join(t.TempDir(), "tenant-workspace")

	err := registry.initializeTenantWorkspace(workspacePath)
	if err != nil {
		t.Fatalf("initializeTenantWorkspace failed: %v", err)
	}

	// Check if file was copied
	copiedFile := filepath.Join(workspacePath, "SOUL.md")
	if _, err := os.Stat(copiedFile); os.IsNotExist(err) {
		t.Error("expected SOUL.md to be copied to tenant workspace")
	}
}
