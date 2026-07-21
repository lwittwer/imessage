package cli

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// resetConfigKind validates enough of a bridge config to choose the reset's
// remote-state policy. It deliberately fails closed: the shell reset must not
// guess that a missing or malformed config is self-hosted.
func resetConfigKind(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read config: %w", err)
	}
	var config struct {
		Homeserver struct {
			Address string `yaml:"address"`
			Domain  string `yaml:"domain"`
		} `yaml:"homeserver"`
	}
	if err = yaml.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("parse config: %w", err)
	}
	domain := strings.ToLower(strings.TrimSpace(config.Homeserver.Domain))
	address := strings.TrimSpace(config.Homeserver.Address)
	if domain == "beeper.com" || domain == "beeper.local" {
		return "beeper", nil
	}
	if domain != "" {
		return "self-hosted", nil
	}
	if isBeeperHomeserverAddress(address) {
		return "beeper", nil
	}
	return "", fmt.Errorf("config has no identifiable homeserver domain or address")
}

func isBeeperHomeserverAddress(address string) bool {
	if address == "" {
		return false
	}
	parsed, err := url.Parse(address)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "matrix.beeper.com" || host == "matrix.beeper.local"
}
