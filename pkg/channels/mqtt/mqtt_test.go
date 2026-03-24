// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package mqtt

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestNewMQTTChannel(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.MQTTConfig
		wantErr bool
	}{
		{
			name: "valid config",
			cfg: config.MQTTConfig{
				Broker:    "tcp://localhost:1883",
				Topics:    config.FlexibleStringSlice{"test/topic"},
				AllowFrom: config.FlexibleStringSlice{"*"},
			},
			wantErr: false,
		},
		{
			name: "empty broker",
			cfg: config.MQTTConfig{
				Topics: config.FlexibleStringSlice{"test/topic"},
			},
			wantErr: true,
		},
		{
			name: "auto generate client id",
			cfg: config.MQTTConfig{
				Broker:    "tcp://localhost:1883",
				Topics:    config.FlexibleStringSlice{"test/topic"},
				AllowFrom: config.FlexibleStringSlice{"*"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch, err := NewMQTTChannel(tt.cfg, bus.NewMessageBus())
			if (err != nil) != tt.wantErr {
				t.Errorf("NewMQTTChannel() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && ch == nil {
				t.Errorf("NewMQTTChannel() returned nil channel")
			}
		})
	}
}

func TestMQTTChannel_Name(t *testing.T) {
	cfg := config.MQTTConfig{
		Broker:    "tcp://localhost:1883",
		Topics:    config.FlexibleStringSlice{"test"},
		AllowFrom: config.FlexibleStringSlice{"*"},
	}

	ch, err := NewMQTTChannel(cfg, bus.NewMessageBus())
	if err != nil {
		t.Fatalf("Failed to create channel: %v", err)
	}

	if ch.Name() != "mqtt" {
		t.Errorf("Name() = %s, want %s", ch.Name(), "mqtt")
	}
}

func TestMQTTChannel_IsRunning(t *testing.T) {
	cfg := config.MQTTConfig{
		Broker:    "tcp://localhost:1883",
		Topics:    config.FlexibleStringSlice{"test"},
		AllowFrom: config.FlexibleStringSlice{"*"},
	}

	ch, err := NewMQTTChannel(cfg, bus.NewMessageBus())
	if err != nil {
		t.Fatalf("Failed to create channel: %v", err)
	}

	if ch.IsRunning() {
		t.Error("IsRunning() = true, want false (before Start)")
	}
}

func TestMQTTChannel_ConfigDefaults(t *testing.T) {
	tests := []struct {
		name     string
		cfg      config.MQTTConfig
		checkFn  func(*MQTTChannel) bool
		desc     string
	}{
		{
			name: "default max message length",
			cfg: config.MQTTConfig{
				Broker:    "tcp://localhost:1883",
				Topics:    config.FlexibleStringSlice{"test"},
				AllowFrom: config.FlexibleStringSlice{"*"},
			},
			checkFn: func(ch *MQTTChannel) bool {
				return ch.MaxMessageLength() == 256000
			},
			desc: "MQTT should have 256KB max message length",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch, err := NewMQTTChannel(tt.cfg, bus.NewMessageBus())
			if err != nil {
				t.Fatalf("Failed to create channel: %v", err)
			}

			if !tt.checkFn(ch) {
				t.Errorf("%s", tt.desc)
			}
		})
	}
}
