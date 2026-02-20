package tools

import (
	"context"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/bus"
)

type SendCallback func(msg bus.OutboundMessage) error

type MessageTool struct {
	sendCallback   SendCallback
	defaultChannel string
	defaultChatID  string
	sentInRound    bool // Tracks whether a message was sent in the current processing round
}

func NewMessageTool() *MessageTool {
	return &MessageTool{}
}

func (t *MessageTool) Name() string {
	return "message"
}

func (t *MessageTool) Description() string {
	return "Send a message to user on a chat channel. Support rich content like images and interactive cards on platforms like Feishu."
}

func (t *MessageTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"content": map[string]interface{}{
				"type":        "string",
				"description": "The message text or JSON card content to send",
			},
			"media": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Optional: list of local file paths or URLs to images/files to attach",
			},
			"channel": map[string]interface{}{
				"type":        "string",
				"description": "Optional: target channel (telegram, feishu, etc.)",
			},
			"chat_id": map[string]interface{}{
				"type":        "string",
				"description": "Optional: target chat/user ID",
			},
			"msg_type": map[string]interface{}{
				"type":        "string",
				"description": "Optional: platform specific message type (e.g. 'interactive' for Feishu card)",
			},
		},
		"required": []string{"content"},
	}
}

func (t *MessageTool) SetContext(channel, chatID string) {
	t.defaultChannel = channel
	t.defaultChatID = chatID
	t.sentInRound = false // Reset send tracking for new processing round
}

// HasSentInRound returns true if the message tool sent a message during the current round.
func (t *MessageTool) HasSentInRound() bool {
	return t.sentInRound
}

func (t *MessageTool) SetSendCallback(callback SendCallback) {
	t.sendCallback = callback
}

func (t *MessageTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	content, ok := args["content"].(string)
	if !ok {
		return &ToolResult{ForLLM: "content is required", IsError: true}
	}

	channel, _ := args["channel"].(string)
	chatID, _ := args["chat_id"].(string)
	msgType, _ := args["msg_type"].(string)

	var media []string
	if m, ok := args["media"].([]interface{}); ok {
		for _, v := range m {
			if s, ok := v.(string); ok {
				media = append(media, s)
			}
		}
	}

	if channel == "" {
		channel = t.defaultChannel
	}
	if chatID == "" {
		chatID = t.defaultChatID
	}

	if channel == "" || chatID == "" {
		return &ToolResult{ForLLM: "No target channel/chat specified", IsError: true}
	}

	if t.sendCallback == nil {
		return &ToolResult{ForLLM: "Message sending not configured", IsError: true}
	}

	metadata := make(map[string]string)
	if msgType != "" {
		metadata["msg_type"] = msgType
	}

	outMsg := bus.OutboundMessage{
		Channel:  channel,
		ChatID:   chatID,
		Content:  content,
		Media:    media,
		Metadata: metadata,
	}

	if err := t.sendCallback(outMsg); err != nil {
		return &ToolResult{
			ForLLM:  fmt.Sprintf("sending message: %v", err),
			IsError: true,
			Err:     err,
		}
	}

	t.sentInRound = true
	// Silent: user already received the message directly
	return &ToolResult{
		ForLLM: fmt.Sprintf("Message sent to %s:%s", channel, chatID),
		Silent: true,
	}
}
