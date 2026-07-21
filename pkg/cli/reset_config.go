package cli

import (
	"fmt"
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
			Domain string `yaml:"domain"`
		} `yaml:"homeserver"`
	}
	if err = yaml.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("parse config: %w", err)
	}
	domain := strings.TrimSpace(config.Homeserver.Domain)
	if domain == "" {
		return "", fmt.Errorf("config has no homeserver domain")
	}
	if strings.EqualFold(domain, "beeper.com") {
		return "beeper", nil
	}
	return "self-hosted", nil
}
