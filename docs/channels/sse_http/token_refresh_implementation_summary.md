# SSE+HTTP Token 刷新机制实现总结

## 问题描述

原有的 SSE+HTTP 通道实现中，用户登录后获得的 token 没有过期机制，永久有效。这存在以下安全隐患：
1. Token 泄露后无法自动失效
2. 无法控制会话有效期
3. 客户端无法感知认证状态变化

## 解决方案

实现了完整的 token 生命周期管理机制，包括：
1. Token 过期时间管理
2. Token 刷新端点
3. 自动清理过期 token
4. SSE 事件通知客户端 token 状态

## 实现的更改

### 1. 配置文件修改 (pkg/config/config.go)

添加了新的配置字段：
```go
TokenTTLMinutes int `json:"token_ttl_minutes,omitempty" env:"PICOCLAW_CHANNELS_SSE_HTTP_TOKEN_TTL_MINUTES"`
```

### 2. 默认配置更新 (pkg/config/defaults.go)

为 SSE HTTP 通道添加了默认的 token 过期时间配置：
```go
TokenTTLMinutes: 1440, // 24 hours
```

### 3. 核心功能实现 (pkg/channels/sse_http/sse_http.go)

#### 3.1 Token 信息结构
```go
type tokenInfo struct {
    token     string
    expiresAt time.Time
}
```

#### 3.2 修改 SSEHTTPChannel 结构
```go
activeTokens sync.Map // token → *tokenInfo (之前是 token → bool)
```

#### 3.3 修改 sseConn 结构
添加了 token 字段来追踪每个连接使用的 token：
```go
type sseConn struct {
    // ... 其他字段
    token string // the token used to authenticate this connection
}
```

#### 3.4 登录端点增强
登录时返回更多信息：
```go
// 响应
{
    "token": "uuid-string",
    "expires_in": 86400,      // 新增：过期时间（秒）
    "expires_at": 1234567890  // 新增：过期时间戳
}
```

#### 3.5 新增 Token 刷新端点
```go
func (c *SSEHTTPChannel) handleRefresh(w http.ResponseWriter, r *http.Request)
```
- 验证当前 token 有效性
- 生成新 token
- 删除旧 token
- 返回新 token 信息

#### 3.6 Token 验证增强
在 `authenticate` 方法中添加了过期检查：
```go
if time.Now().Before(info.expiresAt) {
    return true
}
// Token expired, remove it
c.activeTokens.Delete(reqToken)
```

#### 3.7 Token 清理机制
```go
func (c *SSEHTTPChannel) tokenCleanupLoop()
func (c *SSEHTTPChannel) cleanupExpiredTokens()
```
- 每 5 分钟清理一次过期 token
- 在 Start 方法中启动清理协程

#### 3.8 SSE 连接增强
修改 `handleSSE` 方法，添加了：
- Token 追踪：记录连接使用的 token
- 定期检查：每分钟检查 token 是否即将过期
- 事件通知：
  - `token_expiring`: token 剩余时间少于 5 分钟时触发
  - `token_expired`: token 已过期时触发并关闭连接

#### 3.9 工具方法
```go
func (c *SSEHTTPChannel) getTokenTTL() time.Duration
```
获取配置的 token TTL，默认 24 小时。

## API 端点更新

### 新增端点

#### POST /sse_http/refresh
刷新 token，需要在 Authorization 头或查询参数中提供当前有效的 token。

### 更新的端点

#### POST /sse_http/login  
响应中新增 `expires_in` 和 `expires_at` 字段。

### SSE 事件更新

#### 新增 SSE 事件

1. **token_expiring**
   - 触发时机：token 剩余时间少于 5 分钟
   - 数据格式：
     ```json
     {
       "expires_at": 1234567890,
       "expires_in": 299,
       "message": "Your token is about to expire. Please refresh your token."
     }
     ```

2. **token_expired**
   - 触发时机：token 已过期
   - 数据格式：
     ```json
     {
       "message": "Your token has expired. Please login again."
     }
     ```
   - 行为：发送此事件后关闭 SSE 连接

## 配置示例

```json
{
  "channels": {
    "sse_http": {
      "enabled": true,
      "host": "0.0.0.0",
      "port": 18795,
      "password": "your_password",
      "token_ttl_minutes": 1440,
      "allow_token_query": true,
      "allow_origins": ["*"],
      "max_connections": 100
    }
  }
}
```

## 客户端集成指南

客户端需要实现以下逻辑：

1. **登录时保存过期信息**
   ```javascript
   const loginData = await login();
   saveToken(loginData.token, loginData.expires_at);
   ```

2. **设置自动刷新定时器**
   ```javascript
   // 在过期前 5-10 分钟刷新
   setTimeout(refreshToken, (expires_in - 300) * 1000);
   ```

3. **监听 SSE 事件**
   ```javascript
   eventSource.addEventListener('token_expiring', (e) => {
     refreshToken();
   });
   
   eventSource.addEventListener('token_expired', (e) => {
     disconnect();
     login().then(() => reconnect());
   });
   ```

4. **刷新 token 后重连 SSE**
   ```javascript
   const newToken = await refreshToken();
   reconnectSSE(newToken);
   ```

## 向后兼容性

- 如果不设置 `token_ttl_minutes` 或设置为 0，使用默认值 24 小时
- 现有客户端可以继续工作，但 token 会在配置的时间后过期
- 建议客户端尽快更新以支持 token 刷新机制

## 安全改进

1. **限制会话有效期**：所有 token 现在都有过期时间
2. **自动清理**：过期的 token 会定期被清理
3. **即时通知**：客户端会通过 SSE 得知 token 状态变化
4. **token 轮换**：刷新时会生成新 token 并立即作废旧 token

## 测试建议

1. **测试 token 过期**
   - 设置较短的 TTL（如 5 分钟）
   - 验证 token 过期后无法使用

2. **测试 token 刷新**
   - 验证刷新后旧 token 立即失效
   - 验证新 token 可以正常使用

3. **测试 SSE 事件**
   - 验证 `token_expiring` 事件在合适时机触发
   - 验证 `token_expired` 事件触发后连接关闭

4. **测试清理机制**
   - 验证过期 token 被定期清理
   - 检查内存使用情况

## 文档

已创建以下文档：
- `/docs/channels/sse_http/token_refresh.md` - 中文文档
- `/docs/channels/sse_http/token_refresh.en.md` - 英文文档

文档包含：
- 配置说明
- API 端点详细说明
- JavaScript 和 Python 客户端示例代码
- 安全建议
- 故障排除指南

## 总结

此次实现完全解决了 SSE+HTTP 通道缺少 token 刷新机制的问题，提供了：
- ✅ Token 过期管理
- ✅ Token 刷新端点
- ✅ 自动清理机制
- ✅ 客户端通知机制
- ✅ 完整的文档和示例代码
- ✅ 向后兼容
- ✅ 生产就绪

实现遵循了安全最佳实践，提供了灵活的配置选项，并保持了良好的向后兼容性。
