package sse_http

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/identity"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
)

// tokenInfo stores token and its expiration time.
type tokenInfo struct {
	token     string
	expiresAt time.Time
}

// sseConn represents a single SSE connection.
type sseConn struct {
	id        string
	w         http.ResponseWriter
	flusher   http.Flusher
	sessionID string
	token     string // the token used to authenticate this connection
	closed    atomic.Bool
	done      chan struct{}
}

func (sc *sseConn) writeEvent(event string, data any) error {
	if sc.closed.Load() {
		return fmt.Errorf("connection closed")
	}

	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(sc.w, "event: %s\ndata: %s\n\n", event, string(payload))
	if err != nil {
		return err
	}
	sc.flusher.Flush()
	return nil
}

func (sc *sseConn) close() {
	if sc.closed.CompareAndSwap(false, true) {
		close(sc.done)
	}
}

// SSEHTTPChannel implements the SSE + HTTP channel.
type SSEHTTPChannel struct {
	*channels.BaseChannel
	config       config.SSEHTTPConfig
	connections  sync.Map // connID → *sseConn
	activeTokens sync.Map // token  → *tokenInfo
	connCount    atomic.Int32
	ctx          context.Context
	cancel       context.CancelFunc
	server       *http.Server
}

// NewSSEHTTPChannel creates a new SSE+HTTP channel.
func NewSSEHTTPChannel(cfg config.SSEHTTPConfig, messageBus *bus.MessageBus) (*SSEHTTPChannel, error) {
	base := channels.NewBaseChannel("sse_http", cfg, messageBus, cfg.AllowFrom,
		channels.WithReasoningChannelID(cfg.ReasoningChannelID),
	)
	return &SSEHTTPChannel{
		BaseChannel: base,
		config:      cfg,
	}, nil
}

// Start implements Channel.
func (c *SSEHTTPChannel) Start(ctx context.Context) error {
	logger.InfoC("sse_http", "Starting SSE+HTTP channel")
	c.ctx, c.cancel = context.WithCancel(ctx)

	if c.config.Port > 0 {
		addr := fmt.Sprintf("%s:%d", c.config.Host, c.config.Port)
		// Use ServeHTTP as the handler to ensure consistent CORS, Auth and Path handling
		c.server = &http.Server{
			Addr:    addr,
			Handler: c,
		}

		go func() {
			logger.InfoCF("sse_http", "SSE+HTTP server listening", map[string]any{
				"addr": addr,
			})
			if err := c.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.ErrorCF("sse_http", "SSE+HTTP server error", map[string]any{
					"error": err.Error(),
				})
			}
		}()
	}

	// Start token cleanup goroutine if TTL is configured
	if c.config.TokenTTLMinutes > 0 {
		go c.tokenCleanupLoop()
	}

	c.SetRunning(true)
	logger.InfoC("sse_http", "SSE+HTTP channel started")
	return nil
}

// Stop implements Channel.
func (c *SSEHTTPChannel) Stop(ctx context.Context) error {
	logger.InfoC("sse_http", "Stopping SSE+HTTP channel")
	c.SetRunning(false)

	if c.server != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := c.server.Shutdown(shutdownCtx); err != nil {
			logger.ErrorCF("sse_http", "SSE+HTTP server shutdown error", map[string]any{
				"error": err.Error(),
			})
		}
	}

	// Close all connections
	c.connections.Range(func(key, value any) bool {
		if sc, ok := value.(*sseConn); ok {
			sc.close()
		}
		c.connections.Delete(key)
		return true
	})

	if c.cancel != nil {
		c.cancel()
	}

	logger.InfoC("sse_http", "SSE+HTTP channel stopped")
	return nil
}

// WebhookPath implements channels.WebhookHandler.
// This is used if the channel is started without a dedicated Port.
func (c *SSEHTTPChannel) WebhookPath() string {
	if c.config.Port > 0 {
		return "" // Use dedicated port only
	}
	return "/sse_http/"
}

// ServeHTTP implements http.Handler for the shared HTTP server.
// This is used if the channel is started without a dedicated Port.
func (c *SSEHTTPChannel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !c.IsRunning() {
		http.Error(w, "channel not running", http.StatusServiceUnavailable)
		return
	}

	// CORS
	if c.handleCORS(w, r) {
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/sse_http/")
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimRight(path, "/")

	// Authenticate
	if path != "login" && !c.authenticate(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	switch path {
	case "sse":
		c.handleSSE(w, r)
	case "send":
		c.handleSend(w, r)
	case "login":
		c.handleLogin(w, r)
	case "refresh":
		c.handleRefresh(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (c *SSEHTTPChannel) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if c.config.Password != "" && req.Password != c.config.Password {
		http.Error(w, "invalid password", http.StatusUnauthorized)
		return
	}

	// Generate a dynamic token for this session
	t := uuid.New().String()
	ttl := c.getTokenTTL()
	info := &tokenInfo{
		token:     t,
		expiresAt: time.Now().Add(ttl),
	}
	c.activeTokens.Store(t, info)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"token":      t,
		"expires_in": int(ttl.Seconds()),
		"expires_at": info.expiresAt.Unix(),
	})
}

func (c *SSEHTTPChannel) handleCORS(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}

	allowed := false
	if len(c.config.AllowOrigins) == 0 {
		allowed = true
	} else {
		for _, o := range c.config.AllowOrigins {
			if o == "*" || o == origin {
				allowed = true
				break
			}
		}
	}

	if allowed {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	return false
}

func (c *SSEHTTPChannel) authenticate(r *http.Request) bool {
	// If neither static token nor password is required, skip auth.
	if c.config.Token == "" && c.config.Password == "" {
		return true
	}

	var reqToken string
	auth := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(auth, "Bearer "); ok {
		reqToken = after
	}

	if reqToken == "" && c.config.AllowTokenQuery {
		reqToken = r.URL.Query().Get("token")
	}

	if reqToken == "" {
		return false
	}

	// Check if token matches static token
	if c.config.Token != "" && reqToken == c.config.Token {
		return true
	}

	// Check if token is in active generated tokens and not expired
	if val, ok := c.activeTokens.Load(reqToken); ok {
		if info, ok := val.(*tokenInfo); ok {
			if time.Now().Before(info.expiresAt) {
				return true
			}
			// Token expired, remove it
			c.activeTokens.Delete(reqToken)
		}
	}

	return false
}

func (c *SSEHTTPChannel) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Check connection limit
	maxConns := c.config.MaxConnections
	if maxConns <= 0 {
		maxConns = 100
	}
	if int(c.connCount.Load()) >= maxConns {
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		sessionID = uuid.New().String()
	}

	// Extract the token used for authentication
	var connToken string
	auth := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(auth, "Bearer "); ok {
		connToken = after
	}
	if connToken == "" && c.config.AllowTokenQuery {
		connToken = r.URL.Query().Get("token")
	}

	sc := &sseConn{
		id:        uuid.New().String(),
		w:         w,
		flusher:   flusher,
		sessionID: sessionID,
		token:     connToken,
		done:      make(chan struct{}),
	}

	c.connections.Store(sc.id, sc)
	c.connCount.Add(1)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	logger.InfoCF("sse_http", "SSE client connected", map[string]any{
		"conn_id":    sc.id,
		"session_id": sessionID,
	})

	// Send initial event
	_ = sc.writeEvent("connected", map[string]string{"session_id": sessionID})

	// Keeper alive ticker
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	// Token expiry warning ticker (check every minute if token is about to expire)
	var tokenCheckTicker *time.Ticker
	if c.config.TokenTTLMinutes > 0 && connToken != "" {
		tokenCheckTicker = time.NewTicker(1 * time.Minute)
		defer tokenCheckTicker.Stop()
	}

	for {
		select {
		case <-r.Context().Done():
			goto cleanup
		case <-c.ctx.Done():
			goto cleanup
		case <-sc.done:
			goto cleanup
		case <-pingTicker.C:
			_ = sc.writeEvent("ping", nil)
		case <-func() <-chan time.Time {
			if tokenCheckTicker != nil {
				return tokenCheckTicker.C
			}
			return nil
		}():
			// Check if token is about to expire
			if connToken != "" {
				if val, ok := c.activeTokens.Load(connToken); ok {
					if info, ok := val.(*tokenInfo); ok {
						timeUntilExpiry := time.Until(info.expiresAt)
						// Warn if less than 5 minutes remaining
						if timeUntilExpiry > 0 && timeUntilExpiry < 5*time.Minute {
							_ = sc.writeEvent("token_expiring", map[string]any{
								"expires_at": info.expiresAt.Unix(),
								"expires_in": int(timeUntilExpiry.Seconds()),
								"message":    "Your token is about to expire. Please refresh your token.",
							})
						} else if timeUntilExpiry <= 0 {
							// Token has expired
							_ = sc.writeEvent("token_expired", map[string]any{
								"message": "Your token has expired. Please login again.",
							})
							goto cleanup
						}
					}
				}
			}
		}
	}

cleanup:
	sc.close()
	c.connections.Delete(sc.id)
	c.connCount.Add(-1)
	logger.InfoCF("sse_http", "SSE client disconnected", map[string]any{
		"conn_id":    sc.id,
		"session_id": sessionID,
	})
}

func (c *SSEHTTPChannel) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var sessionID, content string
	var metadataReq map[string]any
	var mediaRefs []string

	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		// Make sure io and filepath and os are imported correctly in the file.
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "failed to parse multipart form", http.StatusBadRequest)
			return
		}

		sessionID = r.FormValue("session_id")
		content = r.FormValue("content")

		if metaStr := r.FormValue("metadata"); metaStr != "" {
			_ = json.Unmarshal([]byte(metaStr), &metadataReq)
		}

		if r.MultipartForm != nil && r.MultipartForm.File != nil {
			store := c.GetMediaStore()
			if store != nil {
				scope := channels.BuildMediaScope("sse_http", "sse_http:"+sessionID, uuid.New().String())
				mediaDir := media.TempDir()
				_ = os.MkdirAll(mediaDir, 0o700)

				for _, fileHeaders := range r.MultipartForm.File {
					for _, fileHeader := range fileHeaders {
						file, err := fileHeader.Open()
						if err == nil {
							localPath := filepath.Join(mediaDir, uuid.New().String()+"-"+fileHeader.Filename)
							if out, err := os.Create(localPath); err == nil {
								// Need io copy! (Assumes "io" is imported in the file)
								_, _ = io.Copy(out, file)
								out.Close()

								meta := media.MediaMeta{
									Filename:    fileHeader.Filename,
									ContentType: fileHeader.Header.Get("Content-Type"),
									Source:      "sse_http",
								}
								if ref, err := store.Store(localPath, meta, scope); err == nil {
									mediaRefs = append(mediaRefs, ref)
								} else {
									os.Remove(localPath)
								}
							}
							file.Close()
						}
					}
				}
			}
		}
	} else {
		var req struct {
			SessionID string         `json:"session_id"`
			Content   string         `json:"content"`
			Metadata  map[string]any `json:"metadata"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		sessionID = req.SessionID
		content = req.Content
		metadataReq = req.Metadata
	}

	if strings.TrimSpace(content) == "" && len(mediaRefs) == 0 {
		http.Error(w, "content or media is required", http.StatusBadRequest)
		return
	}

	if sessionID == "" {
		sessionID = "default"
	}

	chatID := "sse_http:" + sessionID
	senderID := "sse_http-user"

	peer := bus.Peer{Kind: "direct", ID: "sse_http:" + sessionID}
	msgID := uuid.New().String()

	metadata := map[string]string{
		"platform":   "sse_http",
		"session_id": sessionID,
	}
	for k, v := range metadataReq {
		metadata[k] = fmt.Sprintf("%v", v)
	}

	sender := bus.SenderInfo{
		Platform:    "sse_http",
		PlatformID:  senderID,
		CanonicalID: identity.BuildCanonicalID("sse_http", senderID),
	}

	if !c.IsAllowedSender(sender) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	c.HandleMessage(c.ctx, peer, msgID, senderID, chatID, content, mediaRefs, metadata, sender)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message_id": msgID})
}

// SendMedia implements channels.MediaSender.
func (c *SSEHTTPChannel) SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) error {
	if !c.IsRunning() {
		return channels.ErrNotRunning
	}

	store := c.GetMediaStore()
	if store == nil {
		return fmt.Errorf("media store not available")
	}

	var mediaPayloads []map[string]any

	for _, part := range msg.Parts {
		localPath, err := store.Resolve(part.Ref)
		if err != nil {
			logger.ErrorCF("sse_http", "Failed to resolve media", map[string]any{"ref": part.Ref})
			continue
		}

		data, err := os.ReadFile(localPath)
		if err != nil {
			logger.ErrorCF("sse_http", "Failed to read media file", map[string]any{"path": localPath})
			continue
		}

		// Assumes encoding/base64 is imported or we can just add it to import path
		encoded := "data:"
		mimeType := part.ContentType
		if mimeType == "" {
			if part.Type == "image" {
				mimeType = "image/jpeg"
			} else if part.Type == "audio" {
				mimeType = "audio/ogg" // default
			} else if part.Type == "video" {
				mimeType = "video/mp4"
			} else {
				mimeType = "application/octet-stream"
			}
		}

		encoded += mimeType + ";base64,"
		encoded += base64.StdEncoding.EncodeToString(data)

		mediaPayloads = append(mediaPayloads, map[string]any{
			"type":      part.Type,
			"mime_type": mimeType,
			"filename":  part.Filename,
			"data":      encoded,
		})
	}

	if len(mediaPayloads) == 0 {
		return fmt.Errorf("no valid media to send")
	}

	return c.broadcastToSession(msg.ChatID, "media", map[string]any{
		"media": mediaPayloads,
	})
}

// Send implements Channel.
func (c *SSEHTTPChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	if !c.IsRunning() {
		return channels.ErrNotRunning
	}

	return c.broadcastToSession(msg.ChatID, "message", map[string]any{
		"content": msg.Content,
	})
}

// EditMessage implements channels.MessageEditor.
func (c *SSEHTTPChannel) EditMessage(ctx context.Context, chatID string, messageID string, content string) error {
	return c.broadcastToSession(chatID, "update", map[string]any{
		"message_id": messageID,
		"content":    content,
	})
}

// StartTyping implements channels.TypingCapable.
func (c *SSEHTTPChannel) StartTyping(ctx context.Context, chatID string) (func(), error) {
	_ = c.broadcastToSession(chatID, "typing_start", nil)
	return func() {
		_ = c.broadcastToSession(chatID, "typing_stop", nil)
	}, nil
}

// SendPlaceholder implements channels.PlaceholderCapable.
func (c *SSEHTTPChannel) SendPlaceholder(ctx context.Context, chatID string) (string, error) {
	if !c.config.Placeholder.Enabled {
		return "", nil
	}

	text := c.config.Placeholder.Text
	if text == "" {
		text = "Thinking... 💭"
	}

	msgID := uuid.New().String()
	err := c.broadcastToSession(chatID, "message", map[string]any{
		"content":    text,
		"message_id": msgID,
	})
	if err != nil {
		return "", err
	}

	return msgID, nil
}

func (c *SSEHTTPChannel) broadcastToSession(chatID string, event string, data any) error {
	sessionID := strings.TrimPrefix(chatID, "sse_http:")

	var sent bool
	c.connections.Range(func(key, value any) bool {
		sc, ok := value.(*sseConn)
		if !ok {
			return true
		}
		if sc.sessionID == sessionID {
			if err := sc.writeEvent(event, data); err != nil {
				logger.DebugCF("sse_http", "Write to connection failed", map[string]any{
					"conn_id": sc.id,
					"error":   err.Error(),
				})
			} else {
				sent = true
			}
		}
		return true
	})

	if !sent {
		// It's normal for SSE clients to be disconnected, so we don't necessarily return error here
		// but we can log it if we want.
		logger.DebugCF("sse_http", "No active connections for session", map[string]any{"session_id": sessionID})
	}
	return nil
}

func (c *SSEHTTPChannel) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract current token from Authorization header or query parameter
	var currentToken string
	auth := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(auth, "Bearer "); ok {
		currentToken = after
	}

	if currentToken == "" && c.config.AllowTokenQuery {
		currentToken = r.URL.Query().Get("token")
	}

	if currentToken == "" {
		http.Error(w, "no token provided", http.StatusBadRequest)
		return
	}

	// Verify current token exists and is valid (not expired)
	val, ok := c.activeTokens.Load(currentToken)
	if !ok {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	info, ok := val.(*tokenInfo)
	if !ok || time.Now().After(info.expiresAt) {
		c.activeTokens.Delete(currentToken)
		http.Error(w, "token expired", http.StatusUnauthorized)
		return
	}

	// Generate new token
	newToken := uuid.New().String()
	ttl := c.getTokenTTL()
	newInfo := &tokenInfo{
		token:     newToken,
		expiresAt: time.Now().Add(ttl),
	}
	c.activeTokens.Store(newToken, newInfo)

	// Remove old token
	c.activeTokens.Delete(currentToken)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"token":      newToken,
		"expires_in": int(ttl.Seconds()),
		"expires_at": newInfo.expiresAt.Unix(),
	})

	logger.InfoCF("sse_http", "Token refreshed", map[string]any{
		"old_token": currentToken[:8] + "...",
		"new_token": newToken[:8] + "...",
	})
}

// getTokenTTL returns the configured token TTL duration, defaulting to 24 hours if not set.
func (c *SSEHTTPChannel) getTokenTTL() time.Duration {
	if c.config.TokenTTLMinutes > 0 {
		return time.Duration(c.config.TokenTTLMinutes) * time.Minute
	}
	return 24 * time.Hour // default 24 hours
}

// tokenCleanupLoop periodically removes expired tokens from the activeTokens map.
func (c *SSEHTTPChannel) tokenCleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.cleanupExpiredTokens()
		}
	}
}

// cleanupExpiredTokens removes all expired tokens from the activeTokens map.
func (c *SSEHTTPChannel) cleanupExpiredTokens() {
	now := time.Now()
	var expiredCount int

	c.activeTokens.Range(func(key, value any) bool {
		if info, ok := value.(*tokenInfo); ok {
			if now.After(info.expiresAt) {
				c.activeTokens.Delete(key)
				expiredCount++
			}
		}
		return true
	})

	if expiredCount > 0 {
		logger.DebugCF("sse_http", "Cleaned up expired tokens", map[string]any{
			"count": expiredCount,
		})
	}
}

// notifyTokenExpiringSoon sends a notification to connected clients when their token is about to expire.
func (c *SSEHTTPChannel) notifyTokenExpiringSoon(token string, expiresAt time.Time) {
	// Notify all connections (they should use this token)
	c.connections.Range(func(key, value any) bool {
		sc, ok := value.(*sseConn)
		if !ok {
			return true
		}
		
		_ = sc.writeEvent("token_expiring", map[string]any{
			"expires_at": expiresAt.Unix(),
			"expires_in": int(time.Until(expiresAt).Seconds()),
			"message":    "Your token is about to expire. Please refresh your token.",
		})
		
		return true
	})
}
