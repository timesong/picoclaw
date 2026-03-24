# PicoClaw - AI Agent Development Guide

PicoClaw is an ultra-lightweight personal AI assistant written in Go. This guide helps AI coding agents understand the project structure, conventions, and workflows.

## Project Overview

- **Module**: `github.com/sipeed/picoclaw`
- **Go Version**: 1.25+
- **Main Binary**: `picoclaw` (CLI), `picoclaw-launcher` (TUI/Web)
- **License**: MIT

## Project Structure

```
picoclaw/
├── cmd/
│   ├── picoclaw/              # Main CLI entry point
│   │   └── internal/*/         # Subcommands (agent, auth, cron, skills, etc.)
│   └── picoclaw-launcher-tui/  # TUI launcher
├── pkg/                        # Core libraries
│   ├── agent/                  # Agent loop, registry, context management
│   ├── channels/               # Communication channels (Telegram, Discord, Slack, etc.)
│   ├── providers/              # LLM providers (OpenAI, Anthropic, Claude, Codex, etc.)
│   ├── tools/                  # MCP tools (web_search, file_read, message, spawn, etc.)
│   ├── skills/                 # Skill system (discovery, installation, registry)
│   ├── routing/                # Message routing to agents
│   ├── session/                # Session history and summarization
│   ├── bus/                    # Message bus (inbound/outbound)
│   ├── config/                 # Configuration management
│   └── logger/                 # Logging utilities
├── web/                        # Web/TUI launcher
│   ├── backend/                # Go backend for launcher
│   └── frontend/               # React/Vite frontend
├── config/                     # Default configurations
└── scripts/                    # Build/test scripts
```

## Build, Lint, Test Commands

### Build

```bash
make build              # Build for current platform (runs generate first)
make generate           # Run go generate only
make build-all          # Build for all platforms
make build-launcher     # Build web/TUI launcher
make install            # Install to ~/.local/bin
make clean              # Remove build artifacts
```

### Lint & Format

```bash
make lint               # Run golangci-lint
make fmt                # Format code with golangci-lint fmt
make fix                # Auto-fix linting issues
make vet                # Run go vet
make check              # Full check: deps + fmt + vet + test
```

### Test

```bash
make test                           # Run all tests
go test -v ./pkg/agent/...          # Test specific package
go test -run TestName ./pkg/agent/  # Run single test by name
go test -run TestAgentLoop ./...    # Run tests matching pattern
go test -bench=. ./pkg/agent/       # Run benchmarks
go test -coverprofile=cov.out ./... # Coverage report
go tool cover -html=cov.out         # View coverage in browser
```

### Development

```bash
make run ARGS="--help"              # Build and run CLI
cd web && make dev                  # Start web launcher dev servers
```

## Code Style Guidelines

### File Headers

All Go files start with this header:

```go
// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors
```

### Imports

Order imports using `gci` (configured in `.golangci.yaml`):
1. Standard library
2. Third-party packages
3. Local module (`github.com/sipeed/picoclaw/...`)

```go
import (
	"context"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)
```

### Formatting

- Use `gofmt` with `simplify: true`
- Line length: 120 characters max
- Use `any` instead of `interface{}`
- Use `a[b:]` instead of `a[b:len(a)]`
- Indent with tabs

### Naming Conventions

| Type | Convention | Example |
|------|------------|---------|
| Packages | lowercase, no underscores | `agent`, `channels` |
| Types/Structs | PascalCase | `AgentLoop`, `MessageBus` |
| Functions/Methods | PascalCase | `ProcessMessage`, `NewAgentLoop` |
| Variables | camelCase | `msgBus`, `defaultResponse` |
| Constants | UPPER_SNAKE_CASE | `DEFAULT_MAX_ITERATIONS` |
| Tests | `TestFuncName_SubCase` | `TestAgentLoop_ProcessMessage` |

### Error Handling

- Always return errors, don't panic (except in main for fatal errors)
- Use `fmt.Errorf` with `%w` for wrapping:
  ```go
  return nil, fmt.Errorf("failed to create provider: %w", err)
  ```
- Check errors immediately after function calls
- Use `errors.Is` and `errors.As` for error type checking

### Logging

Use the structured logger from `pkg/logger`:

```go
import "github.com/sipeed/picoclaw/pkg/logger"

logger.DebugCF("agent", "Processing message", map[string]any{
    "channel": msg.Channel,
    "chat_id": msg.ChatID,
})
logger.InfoCF("agent", "Message processed", map[string]any{
    "duration": duration.Milliseconds(),
})
logger.WarnCF("agent", "Rate limit approaching", map[string]any{
    "remaining": remaining,
})
logger.ErrorCF("agent", "Failed to process", map[string]any{
    "error": err.Error(),
})
```

### Concurrency

- Use `context.Context` for cancellation timeouts
- Use `sync.RWMutex` for read-heavy shared state
- Use `sync.Map` for dynamic key-based concurrency
- Use `atomic.Bool`/`atomic.Int64` for simple flags
- Prefer channels for goroutine communication
- Always call `defer wg.Done()` when using `sync.WaitGroup`

```go
func worker(ctx context.Context, jobs <-chan Job, results chan<- Result) {
    for {
        select {
        case <-ctx.Done():
            return
        case job, ok := <-jobs:
            if !ok {
                return
            }
            results <- processJob(job)
        }
    }
}
```

### Testing

- Test files: `*_test.go`
- Use `testify` for assertions
- Use table-driven tests for multiple cases
- Mock external dependencies (providers, channels)

```go
func TestAgentLoop_ProcessMessage(t *testing.T) {
    tests := []struct {
        name       string
        input      string
        wantErr    bool
    }{
        {"valid message", "hello", false},
        {"empty message", "", true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // test logic
        })
    }
}
```

### Key Interfaces

```go
// LLMProvider - interface for all LLM providers
type LLMProvider interface {
    Chat(ctx context.Context, messages []Message, tools []ToolDefinition, 
        model string, options map[string]any) (*LLMResponse, error)
    GetDefaultModel() string
}

// Tool - interface for MCP tools
type Tool interface {
    Name() string
    Description() string
    Parameters() json.RawMessage
    Execute(ctx context.Context, args map[string]any, channel, chatID string) *ToolResult
}

// Channel - interface for communication channels
type Channel interface {
    Name() string
    Connect(ctx context.Context) error
    SendMessage(ctx context.Context, msg OutboundMessage) error
}
```

## Common Patterns

### Creating a New Provider

1. Implement `LLMProvider` interface
2. Handle authentication (token refresh if needed)
3. Map messages to provider-specific format
4. Handle streaming responses
5. Implement `SupportsNativeSearch()` if applicable

### Creating a New Tool

1. Implement `Tool` interface
2. Register in `registerSharedTools()` in `pkg/agent/loop.go`
3. Add config option in `config/config.go`
4. Add to tool enablement checks with `cfg.Tools.IsToolEnabled("tool_name")`

### Creating a New Channel

1. Implement `Channel` interface in `pkg/channels/<name>/`
2. Register in `channels/manager.go`
3. Add config section for credentials
4. Handle inbound/outbound message conversion

## Configuration

Config is JSON-based, located at `~/.picoclaw/config.json`. Access via:

```go
import "github.com/sipeed/picoclaw/pkg/config"

cfg := config.GetConfig()
model := cfg.Agents.Defaults.GetModelName()
maxTokens := cfg.Agents.Defaults.GetMaxTokens()
```

## Useful Utils

```go
// pkg/utils
utils.Truncate(str, maxLen)           // Truncate string
utils.IsAudioFile(filename, mimeType) // Check if audio
utils.MergeAPIKeys(key, keys)         // Merge API key sources
```

## Git Workflow

- Branch from `main`, target `main` for PRs
- Use conventional commits: `feat:`, `fix:`, `docs:`, `refactor:`
- Run `make check` before committing
- Disclose AI assistance in PR template

## Key Dependencies

- `github.com/spf13/cobra` - CLI framework
- `github.com/rs/zerolog` - Structured logging
- `github.com/modelcontextprotocol/go-sdk` - MCP support
- `modernc.org/sqlite` - SQLite storage
- `github.com/stretchr/testify` - Testing
