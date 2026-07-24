package cli

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type resetConfig struct {
	Homeserver struct {
		Address string `yaml:"address"`
		Domain  string `yaml:"domain"`
	} `yaml:"homeserver"`
	Appservice struct {
		ID string `yaml:"id"`
	} `yaml:"appservice"`
	Database struct {
		Type string `yaml:"type"`
		URI  string `yaml:"uri"`
	} `yaml:"database"`
	Network struct {
		CloudKitBackfill bool   `yaml:"cloudkit_backfill"`
		BackfillSource   string `yaml:"backfill_source"`
	} `yaml:"network"`
}

func readResetConfig(path string) (*resetConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var config resetConfig
	if err = yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &config, nil
}

// resetConfigKind validates enough of a bridge config to choose the reset's
// remote-state policy. It deliberately fails closed: the shell reset must not
// guess that a missing or malformed config is self-hosted.
func resetConfigKind(path string) (string, error) {
	config, err := readResetConfig(path)
	if err != nil {
		return "", err
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

func resetConfigValue(path, key string) (string, error) {
	config, err := readResetConfig(path)
	if err != nil {
		return "", err
	}
	var value string
	switch key {
	case "appservice-id":
		value = config.Appservice.ID
	case "database-type":
		value = config.Database.Type
	case "database-uri":
		value = config.Database.URI
	case "network-cloudkit-backfill":
		return fmt.Sprintf("%t", config.Network.CloudKitBackfill), nil
	case "network-backfill-source":
		return strings.TrimSpace(config.Network.BackfillSource), nil
	default:
		return "", fmt.Errorf("unknown config value %q", key)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("config value %q is empty", key)
	}
	return value, nil
}

// mergeResetDatabaseConfig carries the operator-selected database stanza from
// the pre-reset config into a freshly registered Beeper config. Appservice and
// homeserver credentials remain those from the fresh config.
func mergeResetDatabaseConfig(backupPath, freshPath string) error {
	backupData, err := os.ReadFile(backupPath)
	if err != nil {
		return fmt.Errorf("read reset config backup: %w", err)
	}
	freshData, err := os.ReadFile(freshPath)
	if err != nil {
		return fmt.Errorf("read fresh config: %w", err)
	}
	var backupDoc, freshDoc yaml.Node
	if err = yaml.Unmarshal(backupData, &backupDoc); err != nil {
		return fmt.Errorf("parse reset config backup: %w", err)
	}
	if err = yaml.Unmarshal(freshData, &freshDoc); err != nil {
		return fmt.Errorf("parse fresh config: %w", err)
	}
	backupDatabase := yamlMappingValue(&backupDoc, "database")
	freshDatabase := yamlMappingValue(&freshDoc, "database")
	backupAppservice := yamlMappingValue(&backupDoc, "appservice")
	freshAppservice := yamlMappingValue(&freshDoc, "appservice")
	if backupDatabase == nil {
		return fmt.Errorf("reset config backup has no database stanza")
	}
	if freshDatabase == nil {
		return fmt.Errorf("fresh config has no database stanza")
	}
	if backupAppservice == nil || freshAppservice == nil {
		return fmt.Errorf("reset or fresh config has no appservice stanza")
	}
	var databaseValue struct {
		Type string `yaml:"type"`
		URI  string `yaml:"uri"`
	}
	if err = backupDatabase.Decode(&databaseValue); err != nil {
		return fmt.Errorf("decode reset database stanza: %w", err)
	}
	if strings.TrimSpace(databaseValue.Type) == "" || strings.TrimSpace(databaseValue.URI) == "" {
		return fmt.Errorf("reset database stanza is missing type or uri")
	}
	var backupAS, freshAS struct {
		ID string `yaml:"id"`
	}
	if err = backupAppservice.Decode(&backupAS); err != nil {
		return fmt.Errorf("decode reset appservice stanza: %w", err)
	}
	if err = freshAppservice.Decode(&freshAS); err != nil {
		return fmt.Errorf("decode fresh appservice stanza: %w", err)
	}
	if strings.TrimSpace(backupAS.ID) == "" || backupAS.ID != freshAS.ID {
		return fmt.Errorf("fresh appservice id %q does not match reset target %q", freshAS.ID, backupAS.ID)
	}
	*freshDatabase = *backupDatabase

	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(4)
	if err = encoder.Encode(&freshDoc); err != nil {
		return fmt.Errorf("encode merged config: %w", err)
	}
	if err = encoder.Close(); err != nil {
		return fmt.Errorf("close merged config encoder: %w", err)
	}
	var verify yaml.Node
	if err = yaml.Unmarshal(output.Bytes(), &verify); err != nil {
		return fmt.Errorf("validate merged config: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(freshPath), ".config.yaml-*")
	if err != nil {
		return fmt.Errorf("create merged config: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	if err = tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("set merged config permissions: %w", err)
	}
	if _, err = tmp.Write(output.Bytes()); err != nil {
		return fmt.Errorf("write merged config: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("sync merged config: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close merged config: %w", err)
	}
	if err = os.Rename(tmpPath, freshPath); err != nil {
		return fmt.Errorf("replace fresh config: %w", err)
	}
	return nil
}

func yamlMappingValue(document *yaml.Node, key string) *yaml.Node {
	if document == nil || len(document.Content) != 1 {
		return nil
	}
	mapping := document.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
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
