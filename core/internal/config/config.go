package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/liming0791/agentbell/core/internal/event"
)

var ErrNotFound = errors.New("agentbell config not found")

type Config struct {
	DefaultChannel string        `json:"defaultChannel"`
	LarkCLIPath    string        `json:"larkCliPath,omitempty"`
	Notifications  Notifications `json:"notifications"`
	Channels       []Channel     `json:"channels"`
}

type Notifications struct {
	Events         []string `json:"events"`
	IncludeSummary bool     `json:"includeSummary"`
	PrivacyLevel   string   `json:"privacyLevel,omitempty"`
}

type Channel struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	ChatID string `json:"chatId"`
	As     string `json:"as"`
}

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, ErrNotFound
	}
	if err != nil {
		return Config{}, err
	}
	defer file.Close()

	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var result Config
	if err := decoder.Decode(&result); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if err := result.Validate(); err != nil {
		return Config{}, err
	}
	return result, nil
}

func (config Config) Validate() error {
	if config.DefaultChannel == "" {
		return errors.New("defaultChannel is required")
	}
	if config.LarkCLIPath != "" && !filepath.IsAbs(config.LarkCLIPath) {
		return errors.New("larkCliPath must be absolute")
	}
	ids := make(map[string]bool)
	foundDefault := false
	for _, channel := range config.Channels {
		if channel.ID == "" || channel.ChatID == "" {
			return errors.New("each channel requires id and chatId")
		}
		if ids[channel.ID] {
			return fmt.Errorf("duplicate channel id %q", channel.ID)
		}
		ids[channel.ID] = true
		if channel.ID == config.DefaultChannel {
			foundDefault = true
		}
		if channel.Type != "" && channel.Type != "feishu" {
			return fmt.Errorf("unsupported channel type %q", channel.Type)
		}
		if channel.As != "" && channel.As != "bot" && channel.As != "user" {
			return fmt.Errorf("unsupported channel identity %q", channel.As)
		}
	}
	if !foundDefault {
		return fmt.Errorf("default channel %q does not exist", config.DefaultChannel)
	}
	for _, enabledEvent := range config.Notifications.Events {
		if enabledEvent == "" {
			return errors.New("notification event cannot be empty")
		}
	}
	if config.Notifications.PrivacyLevel != "" &&
		config.Notifications.PrivacyLevel != event.PrivacyMetadataOnly &&
		config.Notifications.PrivacyLevel != event.PrivacySummary &&
		config.Notifications.PrivacyLevel != event.PrivacyFull {
		return fmt.Errorf("unsupported privacy level %q", config.Notifications.PrivacyLevel)
	}
	return nil
}

func (config Config) Default() (Channel, bool) {
	for _, channel := range config.Channels {
		if channel.ID == config.DefaultChannel {
			return channel, true
		}
	}
	return Channel{}, false
}

func (config Config) EventEnabled(name string) bool {
	if len(config.Notifications.Events) == 0 {
		return true
	}
	for _, candidate := range config.Notifications.Events {
		if candidate == name {
			return true
		}
	}
	return false
}
