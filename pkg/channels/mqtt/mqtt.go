// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package mqtt

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/identity"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/utils"
)

// MQTTChannel implements the Channel interface for MQTT brokers.
type MQTTChannel struct {
	*channels.BaseChannel
	config   config.MQTTConfig
	client   mqtt.Client
	ctx      context.Context
	cancel   context.CancelFunc
	topics   sync.Map
}

// NewMQTTChannel creates a new MQTT channel.
func NewMQTTChannel(cfg config.MQTTConfig, messageBus *bus.MessageBus) (*MQTTChannel, error) {
	if cfg.Broker == "" {
		return nil, fmt.Errorf("mqtt broker is required")
	}
	// Only auto-generate client ID if it's empty
	if cfg.ClientID == "" {
		cfg.ClientID = "picoclaw-" + uuid.New().String()[:8]
		logger.InfoCF("mqtt", "Auto-generated client ID", map[string]any{
			"client_id": cfg.ClientID,
		})
	}

	base := channels.NewBaseChannel("mqtt", cfg, messageBus, cfg.AllowFrom,
		channels.WithMaxMessageLength(256000),
		channels.WithGroupTrigger(cfg.GroupTrigger),
		channels.WithReasoningChannelID(cfg.ReasoningChannelID),
	)

	return &MQTTChannel{
		BaseChannel: base,
		config:      cfg,
	}, nil
}

// Start connects to the MQTT broker and begins listening.
func (c *MQTTChannel) Start(ctx context.Context) error {
	logger.InfoC("mqtt", "Starting MQTT channel")
	c.ctx, c.cancel = context.WithCancel(ctx)

	opts := mqtt.NewClientOptions().
		AddBroker(c.config.Broker).
		SetClientID(c.config.ClientID).
		SetAutoReconnect(c.config.AutoReconnect).
		SetConnectRetry(true).
		SetConnectRetryInterval(time.Duration(c.config.ReconnectInterval) * time.Second).
		SetOnConnectHandler(c.onConnect).
		SetConnectionLostHandler(c.onConnectionLost).
		SetReconnectingHandler(c.onReconnecting)

	if c.config.Username != "" {
		opts.SetUsername(c.config.Username)
	}
	if c.config.Password != "" {
		opts.SetPassword(c.config.Password)
	}
	if c.config.TLS {
		tlsConfig := &tls.Config{
			ServerName: strings.Split(c.config.Broker, ":")[0],
		}
		if c.config.CACert != "" {
			tlsConfig.Certificates = make([]tls.Certificate, 0)
			tlsConfig.RootCAs = x509.NewCertPool()
			tlsConfig.RootCAs.AppendCertsFromPEM([]byte(c.config.CACert))
		}
		if c.config.Cert != "" && c.config.Key != "" {
			cert, err := tls.X509KeyPair([]byte(c.config.Cert), []byte(c.config.Key))
			if err != nil {
				logger.ErrorCF("mqtt", "Failed to load TLS cert", map[string]any{
					"error": err.Error(),
				})
			} else {
				tlsConfig.Certificates = append(tlsConfig.Certificates, cert)
			}
		}
		opts.SetTLSConfig(tlsConfig)
	}
	opts.SetOrderMatters(false)
	opts.SetKeepAlive(time.Duration(c.config.KeepAlive) * time.Second)
	cleanSession := false
	if c.config.CleanSession != nil {
		cleanSession = *c.config.CleanSession
	}
	opts.SetCleanSession(cleanSession)

	if c.config.WillTopic != "" {
		opts.SetWill(c.config.WillTopic, c.config.WillMessage, byte(c.config.WillQos), false)
	}

	c.client = mqtt.NewClient(opts)

	token := c.client.Connect()
	if !token.WaitTimeout(30 * time.Second) || token.Error() != nil {
		return fmt.Errorf("mqtt connect failed: %w", token.Error())
	}

	c.SetRunning(true)
	logger.InfoCF("mqtt", "MQTT channel started", map[string]any{
		"broker":  c.config.Broker,
		"client":  c.config.ClientID,
		"topics":  c.config.Topics,
		"qos":     c.config.Qos,
		"tls":     c.config.TLS,
	})
	return nil
}

// Stop disconnects from the MQTT broker.
func (c *MQTTChannel) Stop(ctx context.Context) error {
	logger.InfoC("mqtt", "Stopping MQTT channel")
	c.SetRunning(false)

	if c.client != nil && c.client.IsConnected() {
		c.client.Unsubscribe(c.config.Topics...)
		c.client.Disconnect(250)
	}
	if c.cancel != nil {
		c.cancel()
	}

	logger.InfoC("mqtt", "MQTT channel stopped")
	return nil
}

// Send publishes a message to an MQTT topic.
func (c *MQTTChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	if !c.IsRunning() {
		return channels.ErrNotRunning
	}

	// Use response_topic if configured, otherwise use ChatID
	topic := msg.ChatID
	if c.config.ResponseTopic != "" {
		topic = c.config.ResponseTopic
	}
	
	if topic == "" {
		return fmt.Errorf("topic is empty: %w", channels.ErrSendFailed)
	}

	if strings.TrimSpace(msg.Content) == "" {
		return nil
	}

	token := c.client.Publish(topic, byte(c.config.Qos), false, msg.Content)
	if token.WaitTimeout(30 * time.Second) && token.Error() != nil {
		return fmt.Errorf("mqtt publish failed: %w", token.Error())
	}

	logger.InfoCF("mqtt", "Message published", map[string]any{
		"topic": topic,
		"content_preview": utils.Truncate(msg.Content, 50),
	})
	return nil
}

// onConnect is called when the client connects to the broker.
func (c *MQTTChannel) onConnect(client mqtt.Client) {
	logger.DebugCF("mqtt", "Connected to broker", map[string]any{
		"client": c.config.ClientID,
	})

	for _, topic := range c.config.Topics {
		filter := topic
		if !strings.Contains(filter, "#") && !strings.Contains(filter, "+") {
			filter = topic + "/#"
		}

		token := client.Subscribe(filter, byte(c.config.Qos), c.onMessageReceived)
		if token.WaitTimeout(30 * time.Second) && token.Error() != nil {
			logger.ErrorCF("mqtt", "Failed to subscribe to topic", map[string]any{
				"topic": topic,
				"error": token.Error().Error(),
			})
			continue
		}

		c.topics.Store(filter, true)
		logger.DebugCF("mqtt", "Subscribed to filter", map[string]any{
			"filter": filter,
			"topic":  topic,
		})
	}
}

// onConnectionLost is called when the connection is lost.
func (c *MQTTChannel) onConnectionLost(client mqtt.Client, err error) {
	logger.WarnCF("mqtt", "Connection lost", map[string]any{
		"error": err.Error(),
	})
	c.SetRunning(false)
}

// onReconnecting is called when attempting to reconnect.
func (c *MQTTChannel) onReconnecting(client mqtt.Client, opts *mqtt.ClientOptions) {
	logger.DebugC("mqtt", "Attempting to reconnect...")
}

// onMessageReceived is called when a message is received.
func (c *MQTTChannel) onMessageReceived(client mqtt.Client, msg mqtt.Message) {
	topic := msg.Topic()
	payload := string(msg.Payload())

	logger.InfoCF("mqtt", ">>> RAW MESSAGE RECEIVED <<<", map[string]any{
		"topic":   topic,
		"payload": payload,
		"qos":     msg.Qos(),
	})

	if strings.TrimSpace(payload) == "" {
		return
	}

	parts := strings.Split(topic, "/")
	senderID := parts[len(parts)-1]
	chatID := topic

	sender := bus.SenderInfo{
		Platform:    "mqtt",
		PlatformID:  senderID,
		CanonicalID: identity.BuildCanonicalID("mqtt", senderID),
	}

	if !c.IsAllowedSender(sender) {
		logger.DebugCF("mqtt", "Message rejected by allowlist", map[string]any{
			"sender_id": senderID,
		})
		return
	}

	messageID := uuid.New().String()[:8]
	content := payload

	if len(parts) > 1 && !strings.HasPrefix(parts[0], "dm") {
		respond, cleaned := c.ShouldRespondInGroup(false, content)
		if !respond {
			return
		}
		content = cleaned
	}

	metadata := map[string]string{
		"topic":        topic,
		"platform":     "mqtt",
		"qos":          fmt.Sprintf("%d", msg.Qos()),
		"retained":     fmt.Sprintf("%v", msg.Retained()),
		"response_to":  topic,
		"input_topic":  topic,
	}

	logger.DebugCF("mqtt", "Received message", map[string]any{
		"topic":   topic,
		"sender":  senderID,
		"preview": content[:min(len(content), 50)],
	})

	c.HandleMessage(
		c.ctx,
		bus.Peer{Kind: "topic", ID: topic},
		messageID,
		senderID,
		chatID,
		content,
		nil,
		metadata,
		sender,
	)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
