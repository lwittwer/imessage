package connector

import (
	"strings"
	"testing"

	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
	"github.com/rs/zerolog"
	up "go.mau.fi/util/configupgrade"
	"gopkg.in/yaml.v3"
)

func TestDisableICloudContactsConfigAndUpgrade(t *testing.T) {
	var exampleConfig IMConfig
	if err := yaml.Unmarshal([]byte(ExampleConfig), &exampleConfig); err != nil {
		t.Fatalf("unmarshal example config: %v", err)
	}
	if exampleConfig.DisableICloudContacts {
		t.Fatal("disable_icloud_contacts must default to false")
	}

	var config IMConfig
	if err := yaml.Unmarshal([]byte("disable_icloud_contacts: true\n"), &config); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if !config.DisableICloudContacts {
		t.Fatal("disable_icloud_contacts: true was not applied")
	}

	oldExample := strings.ReplaceAll(ExampleConfig, "disable_icloud_contacts: false\n", "disable_icloud_contacts: true\n")
	if !strings.Contains(oldExample, "disable_icloud_contacts: true") {
		t.Fatal("failed to set the opt-out in simulated old config")
	}
	var base, oldConfig yaml.Node
	if err := yaml.Unmarshal([]byte(ExampleConfig), &base); err != nil {
		t.Fatalf("unmarshal current example: %v", err)
	}
	if err := yaml.Unmarshal([]byte(oldExample), &oldConfig); err != nil {
		t.Fatalf("unmarshal simulated old config: %v", err)
	}
	up.SimpleUpgrader(upgradeConfig).DoUpgrade(up.NewHelper(&base, &oldConfig))

	upgraded, err := yaml.Marshal(&base)
	if err != nil {
		t.Fatalf("marshal upgraded config: %v", err)
	}
	if !strings.Contains(string(upgraded), "disable_icloud_contacts: true") {
		t.Fatalf("upgrade did not preserve the user's opt-out:\n%s", upgraded)
	}
}

func TestDisableICloudContactsSkipsBackgroundRefreshKeepsCachedProfile(t *testing.T) {
	client, store := newLocalProfileSyncTestClient(t)
	client.Main = &IMConnector{Config: IMConfig{DisableICloudContacts: true}}
	stored := loadSingleSharedProfile(t, store)
	client.cacheSharedProfileIfAbsent(stored)

	fetches := 0
	fetcher := testSharedProfileFetcher(func(string, []byte, bool) (rustpushgo.WrappedProfileRecord, error) {
		fetches++
		return rustpushgo.WrappedProfileRecord{DisplayName: "Fetched profile"}, nil
	})
	client.refreshAllSharedProfilesForConnection(zerolog.Nop(), nil, fetcher, 0)
	if fetches != 0 {
		t.Fatalf("background refresh made %d network fetches, want 0", fetches)
	}

	profile := client.lookupSharedProfile(stored.Identifier)
	if profile == nil || profile.DisplayName != "Stored profile" {
		t.Fatalf("cached profile = %#v, want stored profile to remain available", profile)
	}
	persisted := loadSingleSharedProfile(t, store)
	if persisted.DisplayName != "Stored profile" || persisted.UpdatedTS != 0 {
		t.Fatalf("disabled refresh changed persisted profile: %#v", persisted)
	}
}
