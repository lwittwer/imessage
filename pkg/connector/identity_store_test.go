package connector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
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
