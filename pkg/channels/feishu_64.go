//go:build amd64 || arm64 || riscv64 || mips64 || ppc64

package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkdispatcher "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkcontact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/utils"
)

type FeishuChannel struct {
	*BaseChannel
	config   config.FeishuConfig
	client   *lark.Client
	wsClient *larkws.Client

	mu        sync.Mutex
	userCache sync.Map // id -> name
	cancel    context.CancelFunc
}

func NewFeishuChannel(cfg config.FeishuConfig, bus *bus.MessageBus) (*FeishuChannel, error) {
	base := NewBaseChannel("feishu", cfg, bus, cfg.AllowFrom)

	return &FeishuChannel{
		BaseChannel: base,
		config:      cfg,
		client:      lark.NewClient(cfg.AppID, cfg.AppSecret),
		userCache:   sync.Map{},
	}, nil
}

func (c *FeishuChannel) Start(ctx context.Context) error {
	if c.config.AppID == "" || c.config.AppSecret == "" {
		return fmt.Errorf("feishu app_id or app_secret is empty")
	}

	dispatcher := larkdispatcher.NewEventDispatcher(c.config.VerificationToken, c.config.EncryptKey).
		OnP2MessageReceiveV1(c.handleMessageReceive)

	runCtx, cancel := context.WithCancel(ctx)

	c.mu.Lock()
	c.cancel = cancel
	c.wsClient = larkws.NewClient(
		c.config.AppID,
		c.config.AppSecret,
		larkws.WithEventHandler(dispatcher),
	)
	wsClient := c.wsClient
	c.mu.Unlock()

	c.setRunning(true)
	logger.InfoC("feishu", "Feishu channel started (websocket mode)")

	go func() {
		if err := wsClient.Start(runCtx); err != nil {
			logger.ErrorCF("feishu", "Feishu websocket stopped with error", map[string]interface{}{
				"error": err.Error(),
			})
		}
	}()

	return nil
}

func (c *FeishuChannel) Stop(ctx context.Context) error {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.wsClient = nil
	c.mu.Unlock()

	c.setRunning(false)
	logger.InfoC("feishu", "Feishu channel stopped")
	return nil
}

func (c *FeishuChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	if !c.IsRunning() {
		return fmt.Errorf("feishu channel not running")
	}

	if msg.ChatID == "" {
		return fmt.Errorf("chat ID is empty")
	}

	// 1. Handle Media if present (currently only first image)
	if len(msg.Media) > 0 {
		for _, m := range msg.Media {
			if strings.HasSuffix(strings.ToLower(m), ".jpg") ||
				strings.HasSuffix(strings.ToLower(m), ".jpeg") ||
				strings.HasSuffix(strings.ToLower(m), ".png") ||
				strings.HasPrefix(m, "http") {
				if err := c.sendImage(ctx, msg.ChatID, m); err != nil {
					logger.ErrorCF("feishu", "Failed to send image", map[string]interface{}{"error": err.Error(), "path": m})
				}
			}
		}
	}

	// 2. Handle Content type
	msgType := larkim.MsgTypeText
	content := ""

	// Check if metadata specifies message type
	if t, ok := msg.Metadata["msg_type"]; ok {
		msgType = t
		content = msg.Content
	} else if strings.HasPrefix(strings.TrimSpace(msg.Content), "{") {
		// Auto-detect interactive card or post
		var js map[string]interface{}
		if err := json.Unmarshal([]byte(msg.Content), &js); err == nil {
			if _, isCard := js["card"]; isCard {
				msgType = larkim.MsgTypeInteractive
				content = msg.Content
			} else if _, isPost := js["post"]; isPost {
				msgType = larkim.MsgTypePost
				content = msg.Content
			}
		}
	}

	if content == "" {
		// Default to text
		msgType = larkim.MsgTypeText
		payload, err := json.Marshal(map[string]string{"text": msg.Content})
		if err != nil {
			return fmt.Errorf("failed to marshal feishu content: %w", err)
		}
		content = string(payload)
	}

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(msg.ChatID).
			MsgType(msgType).
			Content(content).
			Uuid(fmt.Sprintf("picoclaw-%d", time.Now().UnixNano())).
			Build()).
		Build()

	resp, err := c.client.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to send feishu message: %w", err)
	}

	if !resp.Success() {
		return fmt.Errorf("feishu api error: code=%d msg=%s", resp.Code, resp.Msg)
	}

	logger.DebugCF("feishu", "Feishu message sent", map[string]interface{}{
		"chat_id":  msg.ChatID,
		"msg_type": msgType,
	})

	return nil
}

func (c *FeishuChannel) sendImage(ctx context.Context, chatID, path string) error {
	localPath := path
	if strings.HasPrefix(path, "http") {
		localPath = utils.DownloadFileSimple(path, "feishu_upload")
		if localPath == "" {
			return fmt.Errorf("failed to download image from %s", path)
		}
		defer os.Remove(localPath)
	}

	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open image file: %w", err)
	}
	defer file.Close()

	// 1. Upload image
	req := larkim.NewCreateImageReqBuilder().
		Body(larkim.NewCreateImageReqBodyBuilder().
			ImageType(larkim.ImageTypeMessage).
			Image(file).
			Build()).
		Build()

	resp, err := c.client.Im.V1.Image.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to upload image to feishu: %w", err)
	}

	if !resp.Success() {
		return fmt.Errorf("feishu image upload error: code=%d msg=%s", resp.Code, resp.Msg)
	}

	imageKey := *resp.Data.ImageKey

	// 2. Send image message
	payload, _ := json.Marshal(map[string]string{"image_key": imageKey})
	msgReq := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType(larkim.MsgTypeImage).
			Content(string(payload)).
			Uuid(fmt.Sprintf("picoclaw-img-%d", time.Now().UnixNano())).
			Build()).
		Build()

	msgResp, err := c.client.Im.V1.Message.Create(ctx, msgReq)
	if err != nil {
		return fmt.Errorf("failed to send feishu image message: %w", err)
	}

	if !msgResp.Success() {
		return fmt.Errorf("feishu api error sending image: code=%d msg=%s", msgResp.Code, msgResp.Msg)
	}

	return nil
}

func (c *FeishuChannel) handleMessageReceive(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return nil
	}

	message := event.Event.Message
	sender := event.Event.Sender

	chatID := stringValue(message.ChatId)
	if chatID == "" {
		return nil
	}

	senderID := extractFeishuSenderID(sender)
	if senderID == "" {
		senderID = "unknown"
	}

	content := extractFeishuMessageContent(message)
	if content == "" {
		content = "[empty message]"
	}

	metadata := map[string]string{}
	if messageID := stringValue(message.MessageId); messageID != "" {
		metadata["message_id"] = messageID
	}
	if messageType := stringValue(message.MessageType); messageType != "" {
		metadata["message_type"] = messageType
	}
	if chatType := stringValue(message.ChatType); chatType != "" {
		metadata["chat_type"] = chatType
	}
	if sender != nil && sender.TenantKey != nil {
		metadata["tenant_key"] = *sender.TenantKey
	}

	chatType := stringValue(message.ChatType)
	if chatType == "p2p" {
		metadata["peer_kind"] = "direct"
		metadata["peer_id"] = senderID
	} else {
		metadata["peer_kind"] = "group"
		metadata["peer_id"] = chatID
	}

	// Try to get user nickname
	if sender != nil && sender.SenderId != nil {
		idType := "open_id"
		id := ""
		if sender.SenderId.UserId != nil && *sender.SenderId.UserId != "" {
			idType = "user_id"
			id = *sender.SenderId.UserId
		} else if sender.SenderId.OpenId != nil && *sender.SenderId.OpenId != "" {
			idType = "open_id"
			id = *sender.SenderId.OpenId
		}

		if id != "" {
			nickname := c.getNickname(ctx, idType, id)
			if nickname != "" {
				metadata["sender_name"] = nickname
			}
		}
	}

	logger.InfoCF("feishu", "Feishu message received", map[string]interface{}{
		"sender_id": senderID,
		"chat_id":   chatID,
		"preview":   utils.Truncate(content, 80),
	})

	c.HandleMessage(senderID, chatID, content, nil, metadata)
	return nil
}

func extractFeishuSenderID(sender *larkim.EventSender) string {
	if sender == nil || sender.SenderId == nil {
		return ""
	}

	if sender.SenderId.UserId != nil && *sender.SenderId.UserId != "" {
		return *sender.SenderId.UserId
	}
	if sender.SenderId.OpenId != nil && *sender.SenderId.OpenId != "" {
		return *sender.SenderId.OpenId
	}
	if sender.SenderId.UnionId != nil && *sender.SenderId.UnionId != "" {
		return *sender.SenderId.UnionId
	}

	return ""
}

func (c *FeishuChannel) getNickname(ctx context.Context, idType, id string) string {
	if name, ok := c.userCache.Load(id); ok {
		return name.(string)
	}

	req := larkcontact.NewGetUserReqBuilder().
		UserId(id).
		UserIdType(idType).
		Build()

	resp, err := c.client.Contact.V3.User.Get(ctx, req)
	if err != nil {
		logger.DebugCF("feishu", "Failed to fetch user info", map[string]interface{}{"error": err.Error(), "id": id})
		return ""
	}

	if !resp.Success() || resp.Data == nil || resp.Data.User == nil {
		logger.DebugCF("feishu", "Feishu API error fetching user", map[string]interface{}{"code": resp.Code, "msg": resp.Msg})
		return ""
	}

	nickname := ""
	if resp.Data.User.Name != nil {
		nickname = *resp.Data.User.Name
	} else if resp.Data.User.Nickname != nil {
		nickname = *resp.Data.User.Nickname
	}

	if nickname != "" {
		c.userCache.Store(id, nickname)
	}

	return nickname
}

func extractFeishuMessageContent(message *larkim.EventMessage) string {
	if message == nil || message.Content == nil || *message.Content == "" {
		return ""
	}

	if message.MessageType != nil && *message.MessageType == larkim.MsgTypeText {
		var textPayload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(*message.Content), &textPayload); err == nil {
			return textPayload.Text
		}
	}

	return *message.Content
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
