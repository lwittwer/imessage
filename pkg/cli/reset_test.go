package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/lrhodin/corten-matrix/scripts"
	"gopkg.in/yaml.v3"
)

func embeddedScript(t *testing.T, name string) string {
	t.Helper()
	data, err := scripts.Files.ReadFile(name)
	if err != nil {
		t.Fatalf("read embedded script %s: %v", name, err)
	}
	return string(data)
}

func resetFilesystemHelper(t *testing.T) string {
	t.Helper()
	script := embeddedScript(t, "reset-bridge.sh")
	const startMarker = "# BEGIN RESET FILESYSTEM HELPER\n"
	const endMarker = "# END RESET FILESYSTEM HELPER"
	start := strings.Index(script, startMarker)
	end := strings.Index(script, endMarker)
	if start < 0 || end <= start {
		t.Fatal("reset filesystem helper markers missing")
	}
	return script[start+len(startMarker) : end]
}

func markedShellHelper(t *testing.T, marker string) string {
	t.Helper()
	script := embeddedScript(t, "reset-bridge.sh")
	startMarker := "# BEGIN " + marker + "\n"
	endMarker := "# END " + marker
	start := strings.Index(script, startMarker)
	end := strings.Index(script, endMarker)
	if start < 0 || end <= start {
		t.Fatalf("shell helper markers missing for %s", marker)
	}
	return script[start+len(startMarker) : end]
}

func TestResetRequiresConfirmationBeforeMutation(t *testing.T) {
	script := embeddedScript(t, "reset-bridge.sh")
	stop := strings.Index(script, "echo \"Stopping bridge...\"")
	localConfirm := strings.Index(script, `read -r -p "Type RESET BRIDGE DATA to continue: "`)
	imessageConfirm := strings.Index(script, `read -r -p "Type DELETE IMESSAGE STATE to confirm Apple session deletion: "`)
	remoteConfirm := strings.Index(script, `read -r -p "Type DELETE BEEPER BRIDGE to confirm Beeper deletion: "`)
	externalDBConfirm := strings.Index(script, `read -r -p "Type EXTERNAL DATABASE CLEARED after completing that step: "`)
	remotePreflight := strings.Index(script, `if ! WHOAMI_OUTPUT=$("$BINARY" bbctl whoami 2>/dev/null); then`)
	freshExportCheck := strings.Index(script, `if [ "$ack_token" != "$export_token" ]; then`)
	strictRemoteDelete := strings.Index(script, `if ! "$BINARY" bbctl delete "$bridge"; then`)
	deleteLocal := strings.Index(script, `rm -f -- "$db_path" "$db_path-wal" "$db_path-shm"`)

	if stop < 0 || localConfirm < 0 || imessageConfirm < 0 || remoteConfirm < 0 || externalDBConfirm < 0 ||
		remotePreflight < 0 || freshExportCheck < 0 || strictRemoteDelete < 0 || deleteLocal < 0 {
		t.Fatalf("reset script is missing a required confirmation or mutation marker")
	}
	if remotePreflight > localConfirm || remotePreflight > stop {
		t.Fatalf("Beeper preflight runs too late: preflight=%d confirmation=%d stop=%d", remotePreflight, localConfirm, stop)
	}
	if localConfirm > stop || imessageConfirm > stop || remoteConfirm > stop {
		t.Fatalf("reset can stop the service before all confirmations: local=%d imessage=%d remote=%d stop=%d", localConfirm, imessageConfirm, remoteConfirm, stop)
	}
	if stop > deleteLocal {
		t.Fatalf("local state deletion appears before the service stop")
	}
	if strictRemoteDelete < stop {
		t.Fatalf("remote deletion appears before the service stop")
	}
	if freshExportCheck < stop || freshExportCheck > strictRemoteDelete || freshExportCheck > deleteLocal {
		t.Fatalf("fresh session export is not required inside the deletion boundary")
	}
	if externalDBConfirm < stop || externalDBConfirm > strictRemoteDelete || externalDBConfirm > deleteLocal {
		t.Fatalf("external database acknowledgement is outside the post-export deletion boundary")
	}
	if strictRemoteDelete > deleteLocal {
		t.Fatalf("local deletion appears before strict remote deletion: remote=%d local=%d", strictRemoteDelete, deleteLocal)
	}
	for _, required := range []string{
		"if [ ! -t 0 ]",
		"--local-only",
		"--keep-remote",
		"--delete-remote",
		"--delete-imessage-state",
		"--external-database-cleared",
		"check-restore --without-keychain",
		".reset-session-export-request",
		".reset-session-export-ack",
		"Local file deletion cannot clear that external database.",
		"refusing unsafe reset target",
		"a corten-matrix bridge process is still running; no state was deleted.",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("reset script missing safety guard %q", required)
		}
	}
}

func TestResetPreservesIMessageStateByDefault(t *testing.T) {
	script := embeddedScript(t, "reset-bridge.sh")
	deleteStateGuard := strings.Index(script, `if [ "$DELETE_IMESSAGE_STATE" = true ]; then`)
	deleteSession := strings.LastIndex(script, `rm -f -- "$dir/session.json"`)
	deleteNestedState := strings.LastIndex(script, `"$dir/corten-matrix"`)
	if deleteStateGuard < 0 || deleteSession < deleteStateGuard || deleteNestedState < deleteStateGuard {
		t.Fatalf("iMessage state deletion is not guarded: guard=%d session=%d nested=%d", deleteStateGuard, deleteSession, deleteNestedState)
	}
	for _, forbidden := range []string{
		`rm -rf -- "$dir"`,
		`rm -f -- "$dir/config.yaml" "$dir/session.json"`,
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("default reset has over-broad deletion pattern %q", forbidden)
		}
	}
	if !strings.Contains(script, `rm -f -- "$db_path" "$db_path-wal" "$db_path-shm"`) {
		t.Fatal("reset does not delete the expected SQLite database sidecars")
	}
	for _, stateName := range []string{
		"facetime-state.plist",
		"passwords-state.plist",
		"statuskit-state.plist",
		"statuskit-channel-dates.plist",
		"statuskit-cloud-channel-map.plist",
		"sharedstreams-state.plist",
		"anisette",
	} {
		if !strings.Contains(script, `"$dir/`+stateName+`"`) {
			t.Errorf("explicit iMessage state deletion omits %q", stateName)
		}
	}
}

func TestResetFilesystemBehavior(t *testing.T) {
	helper := resetFilesystemHelper(t)

	t.Run("local-only preserves config and iMessage state", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "custom.db")
		preserved := []string{
			"config.yaml", "registration.yaml", "session.json", "keystore.plist",
			"trustedpeers.plist", "facetime-state.plist", "future-apple-state.bin",
		}
		deleted := []string{"custom.db", "custom.db-wal", "custom.db-shm", "bridge.stdout.log", "bridge.stderr.log"}
		for _, name := range append(append([]string{}, preserved...), deleted...) {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{"logs", "state", "anisette", "corten-matrix"} {
			if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
				t.Fatal(err)
			}
		}

		runResetFilesystemHelper(t, helper, dir, dbPath, false, false)

		for _, name := range append(preserved, "state", "anisette", "corten-matrix") {
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				t.Errorf("preserved path %q missing after default reset: %v", name, err)
			}
		}
		for _, name := range append(deleted, "logs") {
			if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
				t.Errorf("bridge artifact %q still exists after default reset", name)
			}
		}
	})

	t.Run("Beeper cleanup deletes stale config but preserves iMessage state", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "corten-matrix.db")
		for _, name := range []string{
			"config.yaml", "config.reset-backup.yaml", "corten-matrix.db", "session.json", "keystore.plist",
			"trustedpeers.plist", "future-apple-state.bin",
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
				t.Fatal(err)
			}
		}

		runResetFilesystemHelper(t, helper, dir, dbPath, true, false)

		for _, name := range []string{"config.reset-backup.yaml", "session.json", "keystore.plist", "trustedpeers.plist", "future-apple-state.bin"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				t.Errorf("iMessage state %q missing after Beeper cleanup: %v", name, err)
			}
		}
		for _, name := range []string{"config.yaml", "corten-matrix.db"} {
			if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
				t.Errorf("stale bridge path %q still exists after Beeper cleanup", name)
			}
		}
	})

	t.Run("explicit flags delete config and both session layouts", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "corten-matrix.db")
		for _, name := range []string{
			"config.yaml", "session.json", "keystore.plist", "trustedpeers.plist",
			"facetime-state.plist", "passwords-state.plist", "statuskit-state.plist",
			"statuskit-channel-dates.plist", "statuskit-cloud-channel-map.plist",
			"sharedstreams-state.plist",
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{"state", "anisette", "corten-matrix"} {
			if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		unknown := filepath.Join(dir, "unrelated-user-file")
		if err := os.WriteFile(unknown, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}

		runResetFilesystemHelper(t, helper, dir, dbPath, true, true)

		if _, err := os.Stat(unknown); err != nil {
			t.Fatalf("unrelated path was deleted: %v", err)
		}
		for _, name := range []string{"config.yaml", "session.json", "state", "anisette", "corten-matrix"} {
			if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
				t.Errorf("explicitly deleted path %q still exists", name)
			}
		}
	})
}

func TestResetRemotePolicy(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     string
		wantErr  bool
	}{
		{name: "Beeper public domain", contents: "homeserver:\n  domain: \"beeper.com\"\n", want: "beeper"},
		{name: "Beeper generated config", contents: "homeserver:\n  address: https://matrix.beeper.com/_hungryserv/example\n  domain: beeper.local\n", want: "beeper"},
		{name: "Beeper address fallback", contents: "homeserver:\n  address: https://matrix.beeper.com/_hungryserv/example\n", want: "beeper"},
		{name: "self-hosted", contents: "homeserver:\n  domain: matrix.example.org\n", want: "self-hosted"},
		{name: "self-hosted domain is authoritative", contents: "homeserver:\n  address: https://matrix.beeper.com/_hungryserv/proxy\n  domain: matrix.example.org\n", want: "self-hosted"},
		{name: "missing identity", contents: "homeserver: {}\n", wantErr: true},
		{name: "malformed YAML", contents: "homeserver: [\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := resetConfigKind(path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resetConfigKind unexpectedly returned %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resetConfigKind: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("resetConfigKind = %q, want %q", got, tt.want)
			}
		})
	}
	if got, err := resetConfigKind(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatalf("resetConfigKind accepted missing config as %q", got)
	}

	script := embeddedScript(t, "reset-bridge.sh")
	for _, required := range []string{
		`"$BINARY" reset-config-kind "$config"`,
		`"$BINARY" reset-config-value "$config" appservice-id`,
		`if [ "$keep_remote" = true ]; then`,
		`printf 'local-only'`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("reset script missing remote-policy guard %q", required)
		}
	}
}

func TestBridgeBinaryDispatchesResetConfigKind(t *testing.T) {
	mainPath := filepath.Join("..", "..", "cmd", "corten-matrix", "main.go")
	source, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	managementCase := regexp.MustCompile(`(?s)case "setup".*?cli\.RunManagement\(os\.Args\[1\], os\.Args\[2:\]\)`).Find(source)
	if managementCase == nil || !strings.Contains(string(managementCase), `"reset-config-kind"`) ||
		!strings.Contains(string(managementCase), `"reset-config-value"`) ||
		!strings.Contains(string(managementCase), `"reset-merge-database"`) {
		t.Fatal("corten-matrix main does not dispatch the reset config inspector through the management CLI")
	}
}

func TestResetBareDualAccountRequiresExplicitSelection(t *testing.T) {
	scriptPath := filepath.Join("..", "..", "scripts", "reset-bridge.sh")
	root := t.TempDir()
	primary := filepath.Join(root, "corten-matrix")
	secondary := filepath.Join(root, "corten-matrix-1")
	for _, dir := range []string{primary, secondary} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("/bin/bash", scriptPath, "/tmp/corten-test-bin", primary, secondary, "test.bundle")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("bare reset unexpectedly accepted two configured accounts")
	}
	if !strings.Contains(string(output), "both bridge accounts are configured; choose --account 0, 1, or all") {
		t.Fatalf("unexpected refusal: %s", output)
	}
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func runResetFilesystemHelper(t *testing.T, helper, dir, dbPath string, deleteRemote, deleteIMessage bool) {
	t.Helper()
	script := helper + "\ndelete_local_bridge_data \"$1\" \"$2\" \"$3\" \"$4\"\n"
	cmd := exec.Command("/bin/bash", "-c", script, "reset-helper-test", dir, dbPath, boolString(deleteRemote), boolString(deleteIMessage))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("reset filesystem helper failed: %v\n%s", err, output)
	}
}

func TestEmbeddedResetRunsAsSudoTargetUser(t *testing.T) {
	cmd := embeddedScriptCommand("/tmp/reset script.sh", []string{"--account", "1", "--delete-remote"}, 0, "bridge-user")
	want := []string{
		"sudo", "-u", "bridge-user", "-H", "/bin/bash", "/tmp/reset script.sh",
		"--account", "1", "--delete-remote",
	}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("sudo reset command = %#v, want %#v", cmd.Args, want)
	}

	direct := embeddedScriptCommand("/tmp/reset.sh", []string{"--account", "0"}, 501, "")
	wantDirect := []string{"/bin/bash", "/tmp/reset.sh", "--account", "0"}
	if !reflect.DeepEqual(direct.Args, wantDirect) {
		t.Fatalf("direct reset command = %#v, want %#v", direct.Args, wantDirect)
	}
}

func TestResetConfigValuesParseFlowStyleYAML(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.yaml")
	contents := `homeserver: {domain: beeper.local}
appservice: {id: custom-imessage, as_token: old-as, hs_token: old-hs}
database: {type: postgres, uri: "postgres://db/example?sslmode=disable"}
network: {cloudkit_backfill: true, backfill_source: chatdb}
`
	if err := os.WriteFile(config, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"appservice-id":             "custom-imessage",
		"database-type":             "postgres",
		"database-uri":              "postgres://db/example?sslmode=disable",
		"network-cloudkit-backfill": "true",
		"network-backfill-source":   "chatdb",
	} {
		got, err := resetConfigValue(config, key)
		if err != nil {
			t.Fatalf("parse %s: %v", key, err)
		}
		if got != want {
			t.Errorf("parse %s = %q, want %q", key, got, want)
		}
	}
	if got, err := resetConfigKind(config); err != nil || got != "beeper" {
		t.Fatalf("flow-style homeserver classified as %q, %v", got, err)
	}

	script := embeddedScript(t, "reset-bridge.sh")
	if strings.Contains(script, `grep -Eq "^[[:space:]]+type:`) || strings.Contains(script, "read_yaml_scalar") {
		t.Fatal("reset still uses line-oriented YAML parsing for database policy")
	}
}

func TestMergeResetDatabaseConfigPreservesDatabaseOnly(t *testing.T) {
	dir := t.TempDir()
	backup := filepath.Join(dir, "config.reset-backup.yaml")
	fresh := filepath.Join(dir, "config.yaml")
	backupData := `appservice: {id: custom-imessage, as_token: old-as, hs_token: old-hs}
database: {type: postgres, uri: "postgres://db/example", max_open_conns: 23}
`
	freshData := `appservice:
  id: custom-imessage
  as_token: fresh-as
  hs_token: fresh-hs
database:
  type: sqlite3-fk-wal
  uri: file:fresh.db
`
	if err := os.WriteFile(backup, []byte(backupData), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fresh, []byte(freshData), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := mergeResetDatabaseConfig(backup, fresh); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(fresh)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Appservice struct {
			ASToken string `yaml:"as_token"`
			HSToken string `yaml:"hs_token"`
		} `yaml:"appservice"`
		Database struct {
			Type         string `yaml:"type"`
			URI          string `yaml:"uri"`
			MaxOpenConns int    `yaml:"max_open_conns"`
		} `yaml:"database"`
	}
	if err = yaml.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Appservice.ASToken != "fresh-as" || got.Appservice.HSToken != "fresh-hs" {
		t.Fatalf("fresh appservice credentials were overwritten: %#v", got.Appservice)
	}
	if got.Database.Type != "postgres" || got.Database.URI != "postgres://db/example" || got.Database.MaxOpenConns != 23 {
		t.Fatalf("database stanza was not preserved: %#v", got.Database)
	}
	info, err := os.Stat(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("merged config mode = %o, want 600", info.Mode().Perm())
	}
}

func TestMergeResetDatabaseConfigFailureLeavesFreshConfigUntouched(t *testing.T) {
	dir := t.TempDir()
	backup := filepath.Join(dir, "config.reset-backup.yaml")
	fresh := filepath.Join(dir, "config.yaml")
	original := []byte("appservice: [\ndatabase: {type: sqlite3-fk-wal, uri: file:fresh.db}\n")
	if err := os.WriteFile(backup, []byte("database: {type: postgres, uri: postgres://db/example}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fresh, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mergeResetDatabaseConfig(backup, fresh); err == nil {
		t.Fatal("malformed fresh config unexpectedly merged")
	}
	after, err := os.ReadFile(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, original) {
		t.Fatal("failed merge modified the fresh config")
	}
}

func TestResetWhoamiBridgeMatchIsExact(t *testing.T) {
	helper := markedShellHelper(t, "RESET WHOAMI HELPER")
	whoami := "lucas\n  custom-imessage imessage RUNNING\n  custom-imessage-old imessage RUNNING\n"
	for bridge, wantSuccess := range map[string]bool{
		"custom-imessage":     true,
		"custom":              false,
		"custom-imessage-old": true,
		"sh-imessage":         false,
	} {
		cmd := exec.Command("/bin/bash", "-c", helper+"\nprintf '%s' \"$2\" | whoami_has_bridge \"$1\"", "whoami-helper-test", bridge, whoami)
		err := cmd.Run()
		if (err == nil) != wantSuccess {
			t.Errorf("whoami match for %q success=%t, want %t", bridge, err == nil, wantSuccess)
		}
	}
}

func TestResetFirstBridgeTargetWorksWithBashNounset(t *testing.T) {
	helper := markedShellHelper(t, "RESET BRIDGE TARGET HELPER")
	script := "set -u\nSELECTED_BRIDGES=()\n" + helper + "\nbridge_target_is_unique custom-imessage\n"
	cmd := exec.Command("/bin/bash", "-c", script)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("first bridge target failed under nounset: %v\n%s", err, output)
	}
}

func TestBeeperSetupNeverDeletesStateToRepairRegistration(t *testing.T) {
	for _, name := range []string{"install-beeper.sh", "install-beeper-linux.sh"} {
		script := embeddedScript(t, name)
		t.Run(name, func(t *testing.T) {
			for _, forbidden := range []string{
				"\"$BINARY\" bbctl delete",
				"rm -f \"$CONFIG\"",
			} {
				if strings.Contains(script, forbidden) {
					t.Errorf("setup contains automatic destructive recovery command %q", forbidden)
				}
			}
			for _, required := range []string{
				"Reusing it; setup will not delete remote Matrix rooms.",
				"Setup will not delete the config or database automatically.",
				`reset-config-value "$RESET_CONFIG_BACKUP" appservice-id`,
				`CONFIG_WORK="$DATA_DIR/.config.reset-new.yaml"`,
				`reset-merge-database "$RESET_CONFIG_BACKUP" "$CONFIG_WORK"`,
				`mv -f -- "$CONFIG_WORK" "$CONFIG"`,
				"Preserved database configuration restored",
			} {
				if !strings.Contains(script, required) {
					t.Errorf("setup missing safety warning %q", required)
				}
			}
		})
	}
}
