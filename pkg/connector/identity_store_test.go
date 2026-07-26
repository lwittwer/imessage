package connector

import (
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

	saveSessionState(log, PersistedSessionState{
		IDSIdentity: "old-identity",
		IDSKeyCache: "opaque-key-cache",
	})
	saveSessionState(log, persistedSessionStateFromMetadata(&UserLoginMetadata{
		IDSIdentity:     "new-identity",
		APSState:        "new-aps",
		IDSUsers:        "new-users",
		PreferredHandle: "tel:+15555550123",
	}))

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

func TestCheckSessionRestoreKeychainRequirementDefaultsStrict(t *testing.T) {
	if !sessionRestoreRequiresKeychain(nil) {
		t.Fatal("default restore validation must require keychain state")
	}
	if sessionRestoreRequiresKeychain([]bool{false}) {
		t.Fatal("explicit false must disable only the keychain requirement")
	}
	if !sessionRestoreRequiresKeychain([]bool{true}) {
		t.Fatal("explicit true must require keychain state")
	}
}

func TestSessionRestoreRequiresHardwareKeyOutsideMacOS(t *testing.T) {
	withoutKey := PersistedSessionState{}
	withKey := PersistedSessionState{HardwareKey: "hardware"}
	if !sessionRestoreHasRequiredPlatformState(withoutKey, "darwin") {
		t.Fatal("Darwin restore unexpectedly required a hardware key")
	}
	if sessionRestoreHasRequiredPlatformState(withoutKey, "linux") {
		t.Fatal("Linux restore accepted a session without a hardware key")
	}
	if !sessionRestoreHasRequiredPlatformState(withKey, "linux") {
		t.Fatal("Linux restore rejected a session with a hardware key")
	}
	if err := validateSessionRestorePlatformConfig(PersistedSessionState{HardwareKey: "not-base64"}, "darwin"); err != nil {
		t.Fatalf("Darwin restore unexpectedly parsed a hardware key: %v", err)
	}
	rustpushgo.InitLogger()
	if err := validateSessionRestorePlatformConfig(PersistedSessionState{HardwareKey: "not-base64"}, "linux"); err == nil {
		t.Fatal("Linux restore accepted a malformed hardware key")
	}
}

func TestSessionExportAcknowledgementUsesRequestedNonce(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	log := zerolog.Nop()
	requestPath, err := sessionExportMarkerPath(".reset-session-export-request")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(requestPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(requestPath, []byte("nonce-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = saveSessionState(log, PersistedSessionState{IDSIdentity: "identity"}); err != nil {
		t.Fatal(err)
	}
	if err = acknowledgeSessionExport(log); err != nil {
		t.Fatal(err)
	}
	if request, readErr := os.ReadFile(requestPath); readErr != nil || string(request) != "nonce-123\n" {
		t.Fatalf("request marker was consumed by acknowledgement: %q, %v", request, readErr)
	}
	ackPath, err := sessionExportMarkerPath(".reset-session-export-ack")
	if err != nil {
		t.Fatal(err)
	}
	ack, err := os.ReadFile(ackPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(ack) != "nonce-123\n" {
		t.Fatalf("acknowledged nonce = %q, want exact request", ack)
	}
	if err = clearSessionExportAcknowledgement(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(ackPath); !os.IsNotExist(err) {
		t.Fatalf("ack marker still exists after clearing: %v", err)
	}
	if err = acknowledgeSessionExport(log); err != nil {
		t.Fatalf("request could not be re-acknowledged by a later disconnect: %v", err)
	}
	ack, err = os.ReadFile(ackPath)
	if err != nil || string(ack) != "nonce-123\n" {
		t.Fatalf("later acknowledgement = %q, %v", ack, err)
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
