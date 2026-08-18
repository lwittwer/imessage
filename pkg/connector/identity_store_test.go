package connector

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"

	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
)

func TestPersistedSessionStateFromMetadata(t *testing.T) {
	meta := &UserLoginMetadata{
		Platform:                 "macos",
		APSState:                 "aps",
		IDSUsers:                 "users",
		IDSIdentity:              "identity",
		DeviceID:                 "device",
		HardwareKey:              "hardware",
		PreferredHandle:          "mailto:user@example.invalid",
		AccountUsername:          "user@example.invalid",
		AccountHashedPasswordHex: "hash",
		AccountPET:               "pet",
		AccountADSID:             "adsid",
		AccountDSID:              "dsid",
		AccountSPDBase64:         "spd",
		MmeDelegateJSON:          "delegate",
	}

	got := persistedSessionStateFromMetadata(meta)
	if got.IDSIdentity != meta.IDSIdentity || got.APSState != meta.APSState || got.IDSUsers != meta.IDSUsers ||
		got.PreferredHandle != meta.PreferredHandle || got.Platform != meta.Platform ||
		got.HardwareKey != meta.HardwareKey || got.DeviceID != meta.DeviceID ||
		got.AccountUsername != meta.AccountUsername || got.AccountHashedPasswordHex != meta.AccountHashedPasswordHex ||
		got.AccountPET != meta.AccountPET || got.AccountADSID != meta.AccountADSID ||
		got.AccountDSID != meta.AccountDSID || got.AccountSPDBase64 != meta.AccountSPDBase64 ||
		got.MmeDelegateJSON != meta.MmeDelegateJSON {
		t.Fatalf("metadata was not fully copied: %#v", got)
	}
}

func TestUserLoginMetadataFromPersistedSessionState(t *testing.T) {
	state := PersistedSessionState{
		Platform:                 "macos",
		APSState:                 "aps",
		IDSUsers:                 "users",
		IDSIdentity:              "identity",
		DeviceID:                 "device",
		HardwareKey:              "hardware",
		PreferredHandle:          "mailto:user@example.invalid",
		AccountUsername:          "user@example.invalid",
		AccountHashedPasswordHex: "hash",
		AccountPET:               "pet",
		AccountADSID:             "adsid",
		AccountDSID:              "dsid",
		AccountSPDBase64:         "spd",
		MmeDelegateJSON:          "delegate",
	}

	got := userLoginMetadataFromPersistedSessionState(state, "fallback-os")
	if got.Platform != state.Platform || got.APSState != state.APSState ||
		got.IDSUsers != state.IDSUsers || got.IDSIdentity != state.IDSIdentity ||
		got.DeviceID != state.DeviceID || got.HardwareKey != state.HardwareKey ||
		got.PreferredHandle != state.PreferredHandle ||
		got.AccountUsername != state.AccountUsername ||
		got.AccountHashedPasswordHex != state.AccountHashedPasswordHex ||
		got.AccountPET != state.AccountPET || got.AccountADSID != state.AccountADSID ||
		got.AccountDSID != state.AccountDSID || got.AccountSPDBase64 != state.AccountSPDBase64 ||
		got.MmeDelegateJSON != state.MmeDelegateJSON {
		t.Fatalf("persisted session was not fully restored: %#v", got)
	}
	if got.ChatsSynced {
		t.Fatal("reset recovery must force a fresh chat sync")
	}

	state.Platform = ""
	if got = userLoginMetadataFromPersistedSessionState(state, "fallback-os"); got.Platform != "fallback-os" {
		t.Fatalf("empty platform restored as %q, want fallback-os", got.Platform)
	}
}

func TestSaveSessionStateAtomicallyPreservesKeyCache(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	log := zerolog.Nop()

	if err := saveSessionState(log, PersistedSessionState{
		IDSIdentity: "old-identity",
		IDSKeyCache: "opaque-key-cache",
	}); err != nil {
		t.Fatal(err)
	}
	if err := saveSessionState(log, persistedSessionStateFromMetadata(&UserLoginMetadata{
		IDSIdentity:     "new-identity",
		APSState:        "new-aps",
		IDSUsers:        "new-users",
		PreferredHandle: "tel:+15555550123",
	})); err != nil {
		t.Fatal(err)
	}

	path, err := sessionFilePath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got PersistedSessionState
	if err = json.Unmarshal(data, &got); err != nil {
		t.Fatalf("session file is not valid JSON: %v", err)
	}
	if got.IDSIdentity != "new-identity" || got.APSState != "new-aps" || got.IDSUsers != "new-users" {
		t.Fatalf("session backup did not contain latest state: %#v", got)
	}
	if got.IDSKeyCache != "opaque-key-cache" {
		t.Fatalf("IDS key cache was not preserved: %#v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0600 {
		t.Fatalf("session file mode = %o, want 600", gotMode)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".session.json-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary session files were not cleaned up: %v", matches)
	}
}

func TestValidateSessionRestorePlatformConfig(t *testing.T) {
	if err := validateSessionRestorePlatformConfig(PersistedSessionState{}, "darwin"); err != nil {
		t.Fatalf("Darwin restore unexpectedly required a hardware key: %v", err)
	}
	if err := validateSessionRestorePlatformConfig(PersistedSessionState{}, "linux"); err == nil {
		t.Fatal("Linux restore accepted a session without a hardware key")
	}
	rustpushgo.InitLogger()
	if err := validateSessionRestorePlatformConfig(PersistedSessionState{HardwareKey: "not-base64"}, "linux"); err == nil {
		t.Fatal("Linux restore accepted a malformed hardware key")
	}
}

func TestValidateCloudKitRestoreState(t *testing.T) {
	validSPD := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>DsPrsId</key><integer>12345</integer>
<key>adsid</key><string>synthetic-adsid</string>
</dict></plist>`
	validDelegate := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>tokens</key><dict><key>com.apple.mobileme</key><string>synthetic-token</string></dict>
<key>config</key><dict>
<key>com.apple.Dataclass.KeychainSync</key><dict>
<key>escrowProxyUrl</key><string>https://escrow.example.invalid</string>
</dict></dict>
</dict></plist>`
	valid := PersistedSessionState{
		AccountUsername:          "user@example.invalid",
		AccountHashedPasswordHex: "aabbccdd",
		AccountPET:               "pet",
		AccountSPDBase64:         base64.StdEncoding.EncodeToString([]byte(validSPD)),
		MmeDelegateJSON:          validDelegate,
	}
	if err := validateCloudKitRestoreState(valid); err != nil {
		t.Fatalf("valid CloudKit restore state was rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*PersistedSessionState)
	}{
		{"missing username", func(state *PersistedSessionState) { state.AccountUsername = "" }},
		{"invalid hashed password", func(state *PersistedSessionState) { state.AccountHashedPasswordHex = "not-hex" }},
		{"missing PET", func(state *PersistedSessionState) { state.AccountPET = "" }},
		{"invalid SPD base64", func(state *PersistedSessionState) { state.AccountSPDBase64 = "not-base64" }},
		{"invalid SPD plist", func(state *PersistedSessionState) {
			state.AccountSPDBase64 = base64.StdEncoding.EncodeToString([]byte("not-a-plist"))
		}},
		{"SPD missing account identifiers", func(state *PersistedSessionState) {
			state.AccountSPDBase64 = base64.StdEncoding.EncodeToString([]byte(`<?xml version="1.0"?><plist version="1.0"><dict></dict></plist>`))
		}},
		{"missing delegate", func(state *PersistedSessionState) { state.MmeDelegateJSON = "" }},
		{"invalid delegate plist", func(state *PersistedSessionState) { state.MmeDelegateJSON = "not-a-plist" }},
		{"delegate missing keychain config", func(state *PersistedSessionState) {
			state.MmeDelegateJSON = `<?xml version="1.0"?><plist version="1.0"><dict><key>tokens</key><dict><key>token</key><string>value</string></dict><key>config</key><dict></dict></dict></plist>`
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := valid
			test.mutate(&state)
			if err := validateCloudKitRestoreState(state); err == nil {
				t.Fatal("invalid CloudKit restore state was accepted")
			}
		})
	}
}

func TestSaveSessionStateReportsWriteFailure(t *testing.T) {
	dataHome := t.TempDir()
	blocker := filepath.Join(dataHome, "blocked")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", blocker)
	if err := saveSessionState(zerolog.Nop(), PersistedSessionState{IDSIdentity: "identity"}); err == nil {
		t.Fatal("session save did not report an unwritable session path")
	}
}
