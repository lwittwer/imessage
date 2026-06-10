package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"
)

func TestRepairBeeperBotAvatar(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configData := []byte("appservice:\n  bot:\n    avatar: " + legacyBeeperIMessageIconMXC + "\n")
	if err := os.WriteFile(configPath, configData, 0600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	br := &mxmain.BridgeMain{
		ConfigPath: configPath,
		Config: &bridgeconfig.Config{
			AppService: bridgeconfig.AppserviceConfig{
				Bot: bridgeconfig.BotUserConfig{Avatar: legacyBeeperIMessageIconMXC},
			},
		},
	}

	repairBeeperBotAvatar(br)

	if br.Config.AppService.Bot.Avatar != iMessageBridgeIconMXC {
		t.Fatalf("bot avatar = %q, want %q", br.Config.AppService.Bot.Avatar, iMessageBridgeIconMXC)
	}
	if br.Config.AppService.Bot.ParsedAvatar.IsEmpty() {
		t.Fatal("parsed bot avatar was not updated")
	}
	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}
	if strings.Contains(string(updated), legacyBeeperIMessageIconMXC) {
		t.Fatalf("config still contains legacy avatar: %s", updated)
	}
	if !strings.Contains(string(updated), iMessageBridgeIconMXC) {
		t.Fatalf("config does not contain repaired avatar: %s", updated)
	}
}
