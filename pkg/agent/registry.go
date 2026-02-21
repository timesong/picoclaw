package agent

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// AgentRegistry manages multiple agent instances and routes messages to them.
type AgentRegistry struct {
	agents            map[string]*AgentInstance
	resolver          *routing.RouteResolver
	mu                sync.RWMutex
	cfg               *config.Config
	bus               *bus.MessageBus
	provider          providers.LLMProvider
	OnAgentRegistered func(id, workspace string)
	globalTools       []tools.Tool
}

// NewAgentRegistry creates a registry from config, instantiating all agents.
func NewAgentRegistry(
	cfg *config.Config,
	bus *bus.MessageBus,
	provider providers.LLMProvider,
) *AgentRegistry {
	registry := &AgentRegistry{
		agents:   make(map[string]*AgentInstance),
		resolver: routing.NewRouteResolver(cfg),
		cfg:      cfg,
		bus:      bus,
		provider: provider,
	}

	agentConfigs := cfg.Agents.List
	if len(agentConfigs) == 0 {
		implicitAgent := &config.AgentConfig{
			ID:      "main",
			Default: true,
		}
		instance := NewAgentInstance(implicitAgent, &cfg.Agents.Defaults, cfg, provider)
		registry.registerToolsToAgent(instance)
		registry.agents["main"] = instance
		logger.InfoCF("agent", "Created implicit main agent (no agents.list configured)", nil)
	} else {
		for i := range agentConfigs {
			ac := &agentConfigs[i]
			id := routing.NormalizeAgentID(ac.ID)
			instance := NewAgentInstance(ac, &cfg.Agents.Defaults, cfg, provider)
			registry.registerToolsToAgent(instance)
			registry.agents[id] = instance
			logger.InfoCF("agent", "Registered agent",
				map[string]any{
					"agent_id":  id,
					"name":      ac.Name,
					"workspace": instance.Workspace,
					"model":     instance.Model,
				})
		}
	}

	return registry
}

func (r *AgentRegistry) SetOnAgentRegistered(cb func(id, workspace string)) {
	r.mu.Lock()
	r.OnAgentRegistered = cb
	// Collect existing agents to notify
	type agentInfo struct{ id, ws string }
	var existing []agentInfo
	for id, agent := range r.agents {
		existing = append(existing, agentInfo{id, agent.Workspace})
	}
	r.mu.Unlock()

	// Notify for existing agents
	for _, a := range existing {
		cb(a.id, a.ws)
	}
}

// GetAgent returns the agent instance for a given ID.
// If the agent doesn't exist but has a "user-" prefix, it is automatically registered.
func (r *AgentRegistry) GetAgent(agentID string) (*AgentInstance, bool) {
	r.mu.RLock()
	id := routing.NormalizeAgentID(agentID)
	agent, ok := r.agents[id]
	r.mu.RUnlock()
	if ok {
		return agent, true
	}

	// Support dynamic registration for "user-" agents
	if strings.HasPrefix(id, "user-") {
		r.mu.Lock()
		defer r.mu.Unlock()
		// Double check after lock
		if agent, ok := r.agents[id]; ok {
			return agent, true
		}

		// Implement dynamic registration
		logger.InfoCF("agent", "Auto-registering new user agent", map[string]interface{}{
			"agent_id": id,
		})

		// Create virtual config
		// Use a dedicated workspace folder for dynamic tenants to ensure isolation
		workspace := filepath.Join("~", ".picoclaw", "tenants", id)

		ac := &config.AgentConfig{
			ID:        id,
			Name:      id,
			Workspace: workspace,
		}

		instance := NewAgentInstance(ac, &r.cfg.Agents.Defaults, r.cfg, r.provider)

		// Initialize tenant workspace with templates and skills from default workspace
		r.initializeTenantWorkspace(instance.Workspace)

		r.registerToolsToAgent(instance)
		r.agents[id] = instance

		logger.InfoCF("agent", "Registered dynamic agent",
			map[string]interface{}{
				"agent_id":  id,
				"workspace": instance.Workspace,
				"model":     instance.Model,
			})

		// Notify callback outside lock
		if cb := r.OnAgentRegistered; cb != nil {
			go cb(id, instance.Workspace)
		}

		return instance, true
	}

	return nil, false
}

// RegisterGlobalTool registers a tool that should be available to all current and future agents.
func (r *AgentRegistry) RegisterGlobalTool(tool tools.Tool) {
	r.mu.Lock()
	r.globalTools = append(r.globalTools, tool)

	// Also register to all currently running agents
	for _, agent := range r.agents {
		agent.Tools.Register(tool)
	}
	r.mu.Unlock()
}

// registerToolsToAgent registers tools that are shared across all agents.
func (r *AgentRegistry) registerToolsToAgent(agent *AgentInstance) {
	// Web tools
	if searchTool := tools.NewWebSearchTool(tools.WebSearchToolOptions{
		BraveAPIKey:          r.cfg.Tools.Web.Brave.APIKey,
		BraveMaxResults:      r.cfg.Tools.Web.Brave.MaxResults,
		BraveEnabled:         r.cfg.Tools.Web.Brave.Enabled,
		DuckDuckGoMaxResults: r.cfg.Tools.Web.DuckDuckGo.MaxResults,
		DuckDuckGoEnabled:    r.cfg.Tools.Web.DuckDuckGo.Enabled,
		PerplexityAPIKey:     r.cfg.Tools.Web.Perplexity.APIKey,
		PerplexityMaxResults: r.cfg.Tools.Web.Perplexity.MaxResults,
		PerplexityEnabled:    r.cfg.Tools.Web.Perplexity.Enabled,
	}); searchTool != nil {
		agent.Tools.Register(searchTool)
	}
	agent.Tools.Register(tools.NewWebFetchTool(50000))

	// Hardware tools (I2C, SPI)
	agent.Tools.Register(tools.NewI2CTool())
	agent.Tools.Register(tools.NewSPITool())

	// Message tool
	messageTool := tools.NewMessageTool()
	messageTool.SetSendCallback(func(msg bus.OutboundMessage) error {
		r.bus.PublishOutbound(msg)
		return nil
	})
	agent.Tools.Register(messageTool)

	// Spawn tool with allowlist checker
	subagentManager := tools.NewSubagentManager(r.provider, agent.Model, agent.Workspace, r.bus)
	spawnTool := tools.NewSpawnTool(subagentManager)
	currentAgentID := agent.ID
	spawnTool.SetAllowlistChecker(func(targetAgentID string) bool {
		return r.CanSpawnSubagent(currentAgentID, targetAgentID)
	})
	agent.Tools.Register(spawnTool)

	// Register any dynamically added global tools
	for _, t := range r.globalTools {
		agent.Tools.Register(t)
	}

	// Update context builder with the complete tools registry
	agent.ContextBuilder.SetToolsRegistry(agent.Tools)
}

// initializeTenantWorkspace copies bootstrap files and skills from the default workspace to the new tenant workspace.
func (r *AgentRegistry) initializeTenantWorkspace(target string) {
	source := expandHome(r.cfg.Agents.Defaults.Workspace)
	if source == "" || source == target {
		return
	}

	logger.InfoCF("agent", "Initializing tenant workspace from template", map[string]interface{}{
		"source": source,
		"target": target,
	})

	// 1. Copy bootstrap files
	bootstrapFiles := []string{"SOUL.md", "USER.md", "IDENTITY.md", "AGENTS.md", "HEARTBEAT.md"}
	for _, f := range bootstrapFiles {
		srcPath := filepath.Join(source, f)
		dstPath := filepath.Join(target, f)
		if _, err := os.Stat(dstPath); os.IsNotExist(err) {
			if data, err := os.ReadFile(srcPath); err == nil {
				if err := os.WriteFile(dstPath, data, 0644); err == nil {
					logger.DebugCF("agent", "Copied template file", map[string]interface{}{"file": f})
				}
			}
		}
	}

	// 2. Copy skills directory
	srcSkills := filepath.Join(source, "skills")
	dstSkills := filepath.Join(target, "skills")
	if _, err := os.Stat(srcSkills); err == nil {
		if _, err := os.Stat(dstSkills); os.IsNotExist(err) {
			if err := r.copyDir(srcSkills, dstSkills); err == nil {
				logger.DebugCF("agent", "Copied skills directory", nil)
			} else {
				logger.WarnCF("agent", "Failed to copy skills directory", map[string]interface{}{"error": err.Error()})
			}
		}
	}
}

// copyDir recursively copies a directory
func (r *AgentRegistry) copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dst, relPath)
		if info.IsDir() {
			return os.MkdirAll(targetPath, info.Mode())
		}

		return r.copyFile(path, targetPath)
	})
}

// copyFile copies a single file
func (r *AgentRegistry) copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}

	info, err := os.Stat(src)
	if err == nil {
		return os.Chmod(dst, info.Mode())
	}
	return nil
}

// ResolveRoute determines which agent handles the message.
func (r *AgentRegistry) ResolveRoute(input routing.RouteInput) routing.ResolvedRoute {
	return r.resolver.ResolveRoute(input)
}

// ListAgentIDs returns all registered agent IDs.
func (r *AgentRegistry) ListAgentIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.agents))
	for id := range r.agents {
		ids = append(ids, id)
	}
	return ids
}

// CanSpawnSubagent checks if parentAgentID is allowed to spawn targetAgentID.
func (r *AgentRegistry) CanSpawnSubagent(parentAgentID, targetAgentID string) bool {
	parent, ok := r.GetAgent(parentAgentID)
	if !ok {
		return false
	}
	if parent.Subagents == nil || parent.Subagents.AllowAgents == nil {
		return false
	}
	targetNorm := routing.NormalizeAgentID(targetAgentID)
	for _, allowed := range parent.Subagents.AllowAgents {
		if allowed == "*" {
			return true
		}
		if routing.NormalizeAgentID(allowed) == targetNorm {
			return true
		}
	}
	return false
}

// GetDefaultAgent returns the default agent instance.
func (r *AgentRegistry) GetDefaultAgent() *AgentInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if agent, ok := r.agents["main"]; ok {
		return agent
	}
	for _, agent := range r.agents {
		return agent
	}
	return nil
}
