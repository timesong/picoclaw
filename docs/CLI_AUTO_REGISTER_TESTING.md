# CLI 自动注册测试指南

## 问题描述

用户配置了 `auto_register` 后，使用 `picoclaw.exe agent -s cli:1` 启动 CLI 会话，但租户工作区没有被创建。

## 根本原因

之前的实现中，**CLI 频道被硬编码排除**：

```go
// 硬编码排除 (旧代码)
if channel == "cli" || channel == "system" {
    return ""
}
```

这导致即使配置中没有在 `exclude_channels` 列表中包含 `cli`，CLI 也无法使用自动注册功能。

## 修复内容

### 1. 移除硬编码排除 (`pkg/routing/route.go`)

**修改前**:
```go
// Exclude system channels
if channel == "cli" || channel == "system" {
    return ""
}

// Check exclude list
if cfg.ExcludeChannels != nil {
    ...
}
```

**修改后**:
```go
// Check exclude list (controlled by configuration)
if cfg.ExcludeChannels != nil {
    ...
}
```

### 2. 更新配置示例

将默认的 `exclude_channels` 从:
```json
"exclude_channels": ["cli", "system"]
```

改为:
```json
"exclude_channels": ["system"]
```

这样 CLI 频道就可以使用自动注册功能了。

## 测试步骤

### 1. 更新配置文件

编辑 `~/.picoclaw/config.json`，确保 `exclude_channels` 中**没有** `"cli"`：

```json
{
  "agents": {
    "defaults": {
      "auto_register": {
        "enabled": true,
        "pattern": "user-{channel}-{peer_id}",
        "workspace_template": "~/.picoclaw/tenants/{agent_id}",
        "inherit_from_default": true,
        "copy_files": ["SOUL.md", "USER.md", "IDENTITY.md", "AGENT.md"],
        "copy_skills": true,
        "exclude_channels": ["system"]
      }
    }
  }
}
```

### 2. 启动 CLI 会话

```powershell
cd d:\github\picoclaw
.\build\picoclaw.exe agent -s cli:test-user-1
```

### 3. 验证工作区创建

检查是否创建了租户工作区：

```powershell
# Windows
dir $env:USERPROFILE\.picoclaw\tenants\

# 应该看到类似这样的目录:
# user-cli-test-user-1/
```

### 4. 验证文件继承

检查工作区内容：

```powershell
# Windows
dir $env:USERPROFILE\.picoclaw\tenants\user-cli-test-user-1\

# 应该看到从默认工作区复制的文件:
# SOUL.md
# USER.md
# IDENTITY.md
# AGENT.md
# skills/
```

### 5. 查看日志

启动时应该看到类似的日志：

```
[agent] Auto-registering new agent: agent_id=user-cli-test-user-1
[agent] Auto-registered agent successfully: workspace=C:\Users\...\user-cli-test-user-1, model=gpt-4o
```

## 预期行为

### 生成的 Agent ID

根据配置的 `pattern: "user-{channel}-{peer_id}"`：

| CLI 命令 | 生成的 Agent ID |
|----------|----------------|
| `agent -s cli:alice` | `user-cli-alice` |
| `agent -s cli:bob` | `user-cli-bob` |
| `agent -s cli:123` | `user-cli-123` |

### 工作区路径

根据 `workspace_template: "~/.picoclaw/tenants/{agent_id}"`：

| Agent ID | Windows 路径 |
|----------|-------------|
| `user-cli-alice` | `C:\Users\zhangkun\.picoclaw\tenants\user-cli-alice` |
| `user-cli-bob` | `C:\Users\zhangkun\.picoclaw\tenants\user-cli-bob` |

## 故障排除

### 问题 1: 工作区仍未创建

**检查配置**:
```powershell
# 查看当前配置
type $env:USERPROFILE\.picoclaw\config.json | Select-String -Pattern "auto_register" -Context 10
```

确认:
- `"enabled": true`
- `"exclude_channels"` 中**没有** `"cli"`

### 问题 2: 使用了默认工作区而非租户工作区

**检查日志**:
```powershell
# 启动时添加详细日志
.\build\picoclaw.exe agent -s cli:test --verbose
```

查找是否有 "Auto-registering" 日志。如果没有，说明自动注册没有触发。

### 问题 3: 权限错误

**检查目录权限**:
```powershell
# 确保有权限创建目录
New-Item -ItemType Directory -Path "$env:USERPROFILE\.picoclaw\tenants\test" -Force
```

## 配置选项

如果你**不想** CLI 使用自动注册，可以将 `cli` 加回 `exclude_channels`：

```json
{
  "auto_register": {
    "enabled": true,
    "exclude_channels": ["cli", "system"]
  }
}
```

## 使用场景

### 场景 1: CLI 多用户开发测试

```powershell
# Alice 的会话
.\build\picoclaw.exe agent -s cli:alice

# Bob 的会话  
.\build\picoclaw.exe agent -s cli:bob

# 每个用户有独立的工作区和会话历史
```

### 场景 2: 自动化测试

```powershell
# 为每个测试用例创建独立 agent
.\build\picoclaw.exe agent -s cli:test-1
.\build\picoclaw.exe agent -s cli:test-2
.\build\picoclaw.exe agent -s cli:test-3
```

### 场景 3: 客户演示

```powershell
# 为每个客户创建隔离环境
.\build\picoclaw.exe agent -s cli:customer-acme
.\build\picoclaw.exe agent -s cli:customer-contoso
```

## 技术细节

### 路由优先级

当使用 `agent -s cli:user-id` 时：

1. 构造 `RouteInput`:
   ```go
   input := RouteInput{
       Channel: "cli",
       Peer: &RoutePeer{
           ID: "user-id",
           Kind: "direct",
       },
   }
   ```

2. 路由级联检查:
   - Priority 1-6: 无匹配 (没有明确的 binding)
   - **Priority 7: Auto-register** ✅
     - 检查 `enabled = true`
     - 检查 `peer.Kind = "direct"` ✅
     - 检查 `"cli" not in exclude_channels` ✅
     - 生成 agent ID: `"user-cli-user-id"`
   - Priority 8: 不会到达 (已在 Priority 7 匹配)

3. Agent 注册:
   ```go
   registry.GetAgent("user-cli-user-id")
   → shouldAutoRegister() → true
   → autoRegisterAgent() → 创建工作区
   ```

### 并发安全

自动注册使用双重检查锁定模式：

```go
// 第一次检查 (读锁)
r.mu.RLock()
agent, ok := r.agents[id]
r.mu.RUnlock()

if !ok {
    // 获取写锁
    r.mu.Lock()
    // 第二次检查 (防止竞态)
    if agent, ok := r.agents[id]; ok {
        r.mu.Unlock()
        return agent, true
    }
    // 创建新 agent
    ...
    r.mu.Unlock()
}
```

这确保多个并发请求不会创建重复的 agent。

## 相关文档

- [多租户自动注册完整文档](./multi-tenant-auto-registration.md)
- [实现总结](./IMPLEMENTATION_SUMMARY.md)
- [设计文档](./design/multi-tenant-workspace-isolation.md)
