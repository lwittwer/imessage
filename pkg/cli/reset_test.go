package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lrhodin/corten-matrix/scripts"
)

const resetTestBundleID = "com.example.corten-matrix-test"

type resetTestFixture struct {
	root       string
	stateDir   string
	fakeBin    string
	binary     string
	deleteLog  string
	restoreLog string
	serviceLog string
	pgrepLog   string
	script     string
}

func embeddedResetScript(t *testing.T) string {
	t.Helper()
	data, err := scripts.Files.ReadFile("reset-bridge.sh")
	if err != nil {
		t.Fatalf("read embedded reset script: %v", err)
	}
	return string(data)
}

func newResetTestFixture(t *testing.T) *resetTestFixture {
	t.Helper()
	root := t.TempDir()
	fakeBin := filepath.Join(root, "bin")
	if err := os.Mkdir(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}

	deleteLog := filepath.Join(root, "bbctl-delete.log")
	restoreLog := filepath.Join(root, "check-restore.log")
	serviceLog := filepath.Join(root, "systemctl.log")
	pgrepLog := filepath.Join(root, "pgrep.log")
	fakeBinary := filepath.Join(fakeBin, "corten-matrix")
	writeResetTestExecutable(t, fakeBinary, `#!/bin/sh
set -eu
case "${1:-} ${2:-}" in
  "bbctl whoami")
    if [ "${RESET_TEST_WHOAMI_FAIL:-0}" = "1" ]; then
      exit 1
    fi
    printf '%s\n' 'test-user' '  sh-imessage imessage RUNNING'
    ;;
  "bbctl delete")
    printf '%s\n' "${3:-}" >> "$RESET_TEST_DELETE_LOG"
    if [ "${RESET_TEST_DELETE_FAIL:-0}" = "1" ]; then
      exit 1
    fi
    ;;
  "check-restore "|"check-restore --require-keychain")
    printf '%s\n' "$*" >> "$RESET_TEST_RESTORE_LOG"
    if [ "${RESET_TEST_RESTORE_FAIL:-0}" = "1" ]; then
      exit 1
    fi
    if [ "${2:-}" = "--require-keychain" ] && [ "${RESET_TEST_KEYCHAIN_FAIL:-0}" = "1" ]; then
      exit 1
    fi
    ;;
  *)
    echo "unexpected fake corten-matrix command: $*" >&2
    exit 1
    ;;
esac
`)

	// Force the script down its Linux service path while keeping every command
	// that could affect a real service or process local to this fixture.
	for name, body := range map[string]string{
		"uname": "#!/bin/sh\nprintf '%s\\n' Linux\n",
		"systemctl": `#!/bin/sh
printf '%s\n' "$*" >> "$RESET_TEST_SERVICE_LOG"
case " $* " in
  *" stop "*)
    if [ "${RESET_TEST_REFRESH_SESSION:-0}" = "1" ] && [ -f "$RESET_TEST_STATE_DIR/session.json" ]; then
      cp "$RESET_TEST_STATE_DIR/session.json" "$RESET_TEST_STATE_DIR/.session.refresh"
      mv "$RESET_TEST_STATE_DIR/.session.refresh" "$RESET_TEST_STATE_DIR/session.json"
    fi
    ;;
esac
exit 0
`,
		"journalctl": "#!/bin/sh\nexit 0\n",
		"pgrep": `#!/bin/sh
count=0
if [ -f "$RESET_TEST_PGREP_LOG" ]; then
  count=$(cat "$RESET_TEST_PGREP_LOG")
fi
count=$((count + 1))
printf '%s\n' "$count" > "$RESET_TEST_PGREP_LOG"
if [ "$count" -eq 1 ]; then
  exit "${RESET_TEST_PGREP_PRE_STATUS:-1}"
fi
post_count=$((count - 1))
if [ "$post_count" -le "${RESET_TEST_PGREP_RUNNING_ATTEMPTS:-0}" ]; then
  exit 0
fi
exit "${RESET_TEST_PGREP_STATUS:-1}"
`,
		"sqlite3": `#!/bin/sh
if [ "${RESET_TEST_SQLITE_FAIL:-0}" = "1" ]; then
  exit 1
fi
printf '%s\n' "${RESET_TEST_LOGIN_COUNT:-0}"
`,
		"sleep": "#!/bin/sh\nexit 0\n",
	} {
		writeResetTestExecutable(t, filepath.Join(fakeBin, name), body)
	}

	stateDir := filepath.Join(root, "xdg", "corten-matrix")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	scriptPath := filepath.Join(root, "reset-bridge.sh")
	if err := os.WriteFile(scriptPath, []byte(embeddedResetScript(t)), 0o700); err != nil {
		t.Fatal(err)
	}

	return &resetTestFixture{
		root:       root,
		stateDir:   stateDir,
		fakeBin:    fakeBin,
		binary:     fakeBinary,
		deleteLog:  deleteLog,
		restoreLog: restoreLog,
		serviceLog: serviceLog,
		pgrepLog:   pgrepLog,
		script:     scriptPath,
	}
}

func writeResetTestExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}

func (f *resetTestFixture) seedState(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"config.reset-backup.yaml",
		".config.reset-new.yaml",
		"config.yaml.bak.20260818120000",
		"corten-matrix.db",
		"corten-matrix.db-wal",
		"corten-matrix.db-shm",
		"corten-matrix.db-journal",
		"bridge.stdout.log",
		"bridge.stderr.log",
		"session.json",
		"keystore.plist",
		"trustedpeers.plist",
		".preferred-handle",
		"future-apple-state.sentinel",
	} {
		f.writeFile(t, name)
	}
	f.writeConfig(t, "sqlite3-fk-wal", "file:"+filepath.Join(f.stateDir, "corten-matrix.db")+"?_txlock=immediate", "true", "chatdb")
	for _, name := range []string{
		"logs/bridge.log",
		"state/apple-identity.bin",
		"anisette/device-state.bin",
	} {
		f.writeFile(t, name)
	}
}

func (f *resetTestFixture) writeConfig(t *testing.T, databaseType, databaseURI, cloudKitBackfill, backfillSource string) {
	t.Helper()
	contents := fmt.Sprintf(`homeserver:
    address: https://matrix.beeper.com/_hungryserv/test
    domain: beeper.local
database:
    type: %s
    uri: %s
network:
    cloudkit_backfill: %s
    backfill_source: %s
`, databaseType, databaseURI, cloudKitBackfill, backfillSource)
	if err := os.WriteFile(filepath.Join(f.stateDir, "config.yaml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *resetTestFixture) writeFile(t *testing.T, name string) {
	t.Helper()
	path := filepath.Join(f.stateDir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("synthetic-reset-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *resetTestFixture) run(t *testing.T, input string, args ...string) ([]byte, error) {
	t.Helper()
	cmdArgs := []string{f.script, f.binary, resetTestBundleID}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.Command("/bin/bash", cmdArgs...)
	cmd.Dir = f.root
	cmd.Env = f.environment()
	cmd.Stdin = strings.NewReader(input)
	return cmd.CombinedOutput()
}

func (f *resetTestFixture) environment() []string {
	return append(filteredResetTestEnvironment(),
		"HOME="+f.root,
		"XDG_DATA_HOME="+filepath.Dir(f.stateDir),
		"PATH="+f.fakeBin+":/usr/bin:/bin",
		"RESET_TEST_DELETE_LOG="+f.deleteLog,
		"RESET_TEST_RESTORE_LOG="+f.restoreLog,
		"RESET_TEST_SERVICE_LOG="+f.serviceLog,
		"RESET_TEST_PGREP_LOG="+f.pgrepLog,
		"RESET_TEST_STATE_DIR="+f.stateDir,
		"BRIDGE_NAME=",
	)
}

func (f *resetTestFixture) runPTY(t *testing.T, interactions []struct {
	prompt string
	reply  string
}, args ...string) ([]byte, error) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("PTY confirmation coverage uses macOS /usr/bin/script")
	}
	if _, err := os.Stat("/usr/bin/script"); err != nil {
		t.Skipf("/usr/bin/script unavailable: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmdArgs := []string{"-q", "-e", "/dev/null", "/bin/bash", f.script, f.binary, resetTestBundleID}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.CommandContext(ctx, "/usr/bin/script", cmdArgs...)
	cmd.Dir = f.root
	cmd.Env = f.environment()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}

	reader := bufio.NewReader(stdout)
	var output bytes.Buffer
	readUntil := func(marker string) {
		t.Helper()
		for !strings.Contains(output.String(), marker) {
			b, readErr := reader.ReadByte()
			if readErr != nil {
				t.Fatalf("PTY command ended before %q: %v\n%s", marker, readErr, output.String())
			}
			_ = output.WriteByte(b)
		}
	}
	for _, interaction := range interactions {
		readUntil(interaction.prompt)
		if _, err = io.WriteString(stdin, interaction.reply+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	readUntil("Aborted — nothing was changed.")
	_ = stdin.Close()
	_, _ = io.Copy(&output, reader)
	return output.Bytes(), cmd.Wait()
}

func filteredResetTestEnvironment() []string {
	var env []string
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "BRIDGE_NAME=") ||
			strings.HasPrefix(entry, "XDG_DATA_HOME=") ||
			strings.HasPrefix(entry, "HOME=") ||
			strings.HasPrefix(entry, "PATH=") ||
			strings.HasPrefix(entry, "SERVICE_NAME=") {
			continue
		}
		env = append(env, entry)
	}
	return env
}

func assertResetPathExists(t *testing.T, root, name string, want bool) {
	t.Helper()
	_, err := os.Stat(filepath.Join(root, name))
	if want {
		if err != nil {
			t.Errorf("expected %q to remain: %v", name, err)
		}
		return
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected %q to be removed, stat error=%v", name, err)
	}
}

func TestResetDefaultRemovesBridgeArtifactsAndPreservesAppleState(t *testing.T) {
	f := newResetTestFixture(t)
	f.seedState(t)

	output, err := f.run(t, "", "--yes")
	if err != nil {
		t.Fatalf("default reset failed: %v\n%s", err, output)
	}

	for _, name := range []string{
		"config.yaml",
		"config.reset-backup.yaml",
		".config.reset-new.yaml",
		"corten-matrix.db",
		"corten-matrix.db-wal",
		"corten-matrix.db-shm",
		"corten-matrix.db-journal",
		"bridge.stdout.log",
		"bridge.stderr.log",
		"logs",
	} {
		assertResetPathExists(t, f.stateDir, name, false)
	}
	for _, name := range []string{
		"config.yaml.bak.20260818120000",
		"session.json",
		"keystore.plist",
		"trustedpeers.plist",
		".preferred-handle",
		"future-apple-state.sentinel",
		"state",
		"anisette",
	} {
		assertResetPathExists(t, f.stateDir, name, true)
	}
	for _, name := range []string{
		"config.yaml.bak.20260818120000",
		"session.json",
		"keystore.plist",
		"trustedpeers.plist",
		".preferred-handle",
		"future-apple-state.sentinel",
	} {
		data, readErr := os.ReadFile(filepath.Join(f.stateDir, name))
		if readErr != nil {
			t.Fatalf("read preserved %q: %v", name, readErr)
		}
		if got := string(data); got != "synthetic-reset-fixture" {
			t.Fatalf("preserved %q contents changed to %q", name, got)
		}
	}

	deleteArgs, readErr := os.ReadFile(f.deleteLog)
	if readErr != nil {
		t.Fatalf("read fake Beeper delete log: %v", readErr)
	}
	if got := strings.TrimSpace(string(deleteArgs)); got != "sh-imessage" {
		t.Fatalf("Beeper delete target = %q, want exact upstream bridge name sh-imessage", got)
	}
	restoreArgs, readErr := os.ReadFile(f.restoreLog)
	if readErr != nil {
		t.Fatalf("read restore validation log: %v", readErr)
	}
	if got := strings.TrimSpace(string(restoreArgs)); got != "check-restore" {
		t.Fatalf("chat.db reset restore args = %q, want check-restore without keychain requirement", got)
	}
}

func TestResetRejectsUnknownAndRemovedOptionsBeforeMutation(t *testing.T) {
	for _, option := range []string{"--local-only", "--keep-remote", "--account", "--unknown"} {
		t.Run(option, func(t *testing.T) {
			f := newResetTestFixture(t)
			f.seedState(t)

			output, err := f.run(t, "", "--yes", option)
			if err == nil {
				t.Fatalf("reset unexpectedly accepted %s\n%s", option, output)
			}
			if !strings.Contains(string(output), "unknown reset option") {
				t.Fatalf("reset did not reject %s clearly:\n%s", option, output)
			}
			assertResetPathExists(t, f.stateDir, "config.yaml", true)
			assertResetPathExists(t, f.stateDir, "corten-matrix.db", true)
			if _, statErr := os.Stat(f.serviceLog); !os.IsNotExist(statErr) {
				t.Fatalf("service action ran for rejected option %s: %v", option, statErr)
			}
			if _, statErr := os.Stat(f.deleteLog); !os.IsNotExist(statErr) {
				t.Fatalf("remote deletion ran for rejected option %s: %v", option, statErr)
			}
		})
	}
}

func TestResetHelpDoesNotRequireStateOrMutate(t *testing.T) {
	f := newResetTestFixture(t)
	output, err := f.run(t, "", "--help")
	if err != nil {
		t.Fatalf("reset --help failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Usage: corten-matrix reset") {
		t.Fatalf("reset --help did not print usage:\n%s", output)
	}
	if _, statErr := os.Stat(f.serviceLog); !os.IsNotExist(statErr) {
		t.Fatalf("service action ran for --help: %v", statErr)
	}
}

func TestResetRejectsUnsupportedDatabaseBeforeMutation(t *testing.T) {
	tests := []struct {
		name         string
		databaseType string
		databaseURI  string
		want         string
	}{
		{
			name:         "PostgreSQL",
			databaseType: "postgres",
			databaseURI:  "postgres://bridge:secret@localhost/bridge",
			want:         "does not coordinate PostgreSQL",
		},
		{
			name:         "custom SQLite path",
			databaseType: "sqlite3-fk-wal",
			databaseURI:  "file:/tmp/custom-corten-matrix.db?_txlock=immediate",
			want:         "only supports the default SQLite database",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newResetTestFixture(t)
			f.seedState(t)
			f.writeConfig(t, tt.databaseType, tt.databaseURI, "true", "chatdb")

			output, err := f.run(t, "", "--yes")
			if err == nil {
				t.Fatalf("reset unexpectedly accepted %s\n%s", tt.name, output)
			}
			if !strings.Contains(string(output), tt.want) {
				t.Fatalf("reset reported the wrong %s error:\n%s", tt.name, output)
			}
			assertResetPathExists(t, f.stateDir, "config.yaml", true)
			assertResetPathExists(t, f.stateDir, "corten-matrix.db", true)
			if _, statErr := os.Stat(f.serviceLog); !os.IsNotExist(statErr) {
				t.Fatalf("service action ran for unsupported %s: %v", tt.name, statErr)
			}
		})
	}
}

func TestResetRejectsSelfHostedConfigBeforeBeeperOrServiceMutation(t *testing.T) {
	f := newResetTestFixture(t)
	f.seedState(t)
	configPath := filepath.Join(f.stateDir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	config := strings.ReplaceAll(string(data), "https://matrix.beeper.com/_hungryserv/test", "https://matrix.example.test")
	config = strings.ReplaceAll(config, "beeper.local", "example.test")
	if err = os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	output, err := f.run(t, "", "--yes")
	if err == nil {
		t.Fatalf("reset unexpectedly accepted a self-hosted config with working Beeper auth\n%s", output)
	}
	if !strings.Contains(string(output), "Self-hosted installs are out of scope") {
		t.Fatalf("reset did not explain its self-hosted refusal:\n%s", output)
	}
	assertResetPathExists(t, f.stateDir, "config.yaml", true)
	assertResetPathExists(t, f.stateDir, "corten-matrix.db", true)
	if _, statErr := os.Stat(f.serviceLog); !os.IsNotExist(statErr) {
		t.Fatalf("service action ran for self-hosted config: %v", statErr)
	}
	if _, statErr := os.Stat(f.deleteLog); !os.IsNotExist(statErr) {
		t.Fatalf("Beeper deletion ran for self-hosted config: %v", statErr)
	}
}

func TestResetProcessCheckFailsClosedAndRetries(t *testing.T) {
	t.Run("pgrep error preserves state", func(t *testing.T) {
		f := newResetTestFixture(t)
		f.seedState(t)
		t.Setenv("RESET_TEST_PGREP_STATUS", "2")

		output, err := f.run(t, "", "--yes")
		if err == nil {
			t.Fatalf("reset unexpectedly succeeded after pgrep error\n%s", output)
		}
		if !strings.Contains(string(output), "pgrep failed") {
			t.Fatalf("reset did not report pgrep failure:\n%s", output)
		}
		assertResetPathExists(t, f.stateDir, "config.yaml", true)
		assertResetPathExists(t, f.stateDir, "corten-matrix.db", true)
		if _, statErr := os.Stat(f.deleteLog); !os.IsNotExist(statErr) {
			t.Fatalf("remote deletion ran after pgrep failure: %v", statErr)
		}
	})

	t.Run("waits for a slow shutdown", func(t *testing.T) {
		f := newResetTestFixture(t)
		f.seedState(t)
		t.Setenv("RESET_TEST_PGREP_RUNNING_ATTEMPTS", "2")

		output, err := f.run(t, "", "--yes")
		if err != nil {
			t.Fatalf("reset did not wait for shutdown: %v\n%s", err, output)
		}
		attempts, readErr := os.ReadFile(f.pgrepLog)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if got := strings.TrimSpace(string(attempts)); got != "4" {
			t.Fatalf("pgrep attempts = %q, want 4 (one preflight plus three post-stop)", got)
		}
	})

	t.Run("running bridge must refresh session", func(t *testing.T) {
		f := newResetTestFixture(t)
		f.seedState(t)
		t.Setenv("RESET_TEST_PGREP_PRE_STATUS", "0")

		output, err := f.run(t, "", "--yes")
		if err == nil {
			t.Fatalf("reset accepted a stale pre-shutdown session\n%s", output)
		}
		if !strings.Contains(string(output), "session.json was not refreshed") {
			t.Fatalf("reset did not report the stale shutdown export:\n%s", output)
		}
		assertResetPathExists(t, f.stateDir, "config.yaml", true)
		assertResetPathExists(t, f.stateDir, "corten-matrix.db", true)
		if _, statErr := os.Stat(f.deleteLog); !os.IsNotExist(statErr) {
			t.Fatalf("remote deletion ran with a stale shutdown export: %v", statErr)
		}
	})

	t.Run("running bridge accepts atomic session refresh", func(t *testing.T) {
		f := newResetTestFixture(t)
		f.seedState(t)
		t.Setenv("RESET_TEST_PGREP_PRE_STATUS", "0")
		t.Setenv("RESET_TEST_REFRESH_SESSION", "1")

		output, err := f.run(t, "", "--yes")
		if err != nil {
			t.Fatalf("reset rejected an atomically refreshed shutdown export: %v\n%s", err, output)
		}
	})
}

func TestResetCloudKitRequiresTrustCircleValidation(t *testing.T) {
	for _, boolValue := range []string{"true", "True", "TRUE"} {
		t.Run(boolValue, func(t *testing.T) {
			f := newResetTestFixture(t)
			f.seedState(t)
			f.writeConfig(t, "sqlite3-fk-wal", "file:"+filepath.Join(f.stateDir, "corten-matrix.db")+"?_txlock=immediate", boolValue, "cloudkit")
			t.Setenv("RESET_TEST_KEYCHAIN_FAIL", "1")

			output, err := f.run(t, "", "--yes")
			if err == nil {
				t.Fatalf("CloudKit reset unexpectedly accepted missing trust-circle state\n%s", output)
			}
			if !strings.Contains(string(output), "session state is not safely restorable") {
				t.Fatalf("CloudKit reset did not report restore failure:\n%s", output)
			}
			restoreArgs, readErr := os.ReadFile(f.restoreLog)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if got := strings.TrimSpace(string(restoreArgs)); got != "check-restore --require-keychain" {
				t.Fatalf("CloudKit reset restore args = %q, want keychain requirement", got)
			}
			assertResetPathExists(t, f.stateDir, "config.yaml", true)
			assertResetPathExists(t, f.stateDir, "corten-matrix.db", true)
			if _, statErr := os.Stat(f.deleteLog); !os.IsNotExist(statErr) {
				t.Fatalf("remote deletion ran after CloudKit restore failure: %v", statErr)
			}
		})
	}
	t.Run("invalid bool fails closed", func(t *testing.T) {
		f := newResetTestFixture(t)
		f.seedState(t)
		f.writeConfig(t, "sqlite3-fk-wal", "file:"+filepath.Join(f.stateDir, "corten-matrix.db")+"?_txlock=immediate", "enabled", "cloudkit")

		output, err := f.run(t, "", "--yes")
		if err == nil {
			t.Fatalf("reset unexpectedly accepted an invalid CloudKit boolean\n%s", output)
		}
		if !strings.Contains(string(output), "invalid cloudkit_backfill") {
			t.Fatalf("reset did not report invalid CloudKit config:\n%s", output)
		}
		if _, statErr := os.Stat(f.serviceLog); !os.IsNotExist(statErr) {
			t.Fatalf("service action ran for invalid CloudKit config: %v", statErr)
		}
	})
}

func TestResetNeverLoggedInState(t *testing.T) {
	t.Run("zero logins can reset", func(t *testing.T) {
		f := newResetTestFixture(t)
		f.seedState(t)
		if err := os.Remove(filepath.Join(f.stateDir, "session.json")); err != nil {
			t.Fatal(err)
		}
		t.Setenv("RESET_TEST_LOGIN_COUNT", "0")

		output, err := f.run(t, "", "--yes")
		if err != nil {
			t.Fatalf("never-logged-in reset failed: %v\n%s", err, output)
		}
		if !strings.Contains(string(output), "No saved Apple/iMessage login exists") {
			t.Fatalf("never-logged-in reset did not explain the state:\n%s", output)
		}
	})

	t.Run("database login without session fails closed", func(t *testing.T) {
		f := newResetTestFixture(t)
		f.seedState(t)
		if err := os.Remove(filepath.Join(f.stateDir, "session.json")); err != nil {
			t.Fatal(err)
		}
		t.Setenv("RESET_TEST_LOGIN_COUNT", "1")

		output, err := f.run(t, "", "--yes")
		if err == nil {
			t.Fatalf("reset unexpectedly deleted a DB-backed login without session.json\n%s", output)
		}
		if !strings.Contains(string(output), "--delete-imessage-state") {
			t.Fatalf("reset omitted the explicit fresh-login escape hatch:\n%s", output)
		}
		assertResetPathExists(t, f.stateDir, "config.yaml", true)
		assertResetPathExists(t, f.stateDir, "corten-matrix.db", true)
		if _, statErr := os.Stat(f.deleteLog); !os.IsNotExist(statErr) {
			t.Fatalf("remote deletion ran without restorable login state: %v", statErr)
		}
	})
}

func TestResetDeleteAppleStateRequiresExplicitConfirmation(t *testing.T) {
	t.Run("declined confirmation leaves everything untouched", func(t *testing.T) {
		f := newResetTestFixture(t)
		f.seedState(t)

		output, err := f.runPTY(t, []struct {
			prompt string
			reply  string
		}{
			{prompt: "Type 'reset' to confirm:", reply: "no"},
		}, "--delete-imessage-state")
		if err == nil {
			t.Fatalf("reset unexpectedly succeeded after declined confirmation\n%s", output)
		}
		if !strings.Contains(string(output), "Type 'reset' to confirm:") {
			t.Fatalf("reset did not reach the primary confirmation prompt:\n%s", output)
		}
		for _, name := range []string{
			"config.yaml",
			"corten-matrix.db",
			"session.json",
			"keystore.plist",
			"trustedpeers.plist",
			"future-apple-state.sentinel",
			"state",
			"anisette",
		} {
			assertResetPathExists(t, f.stateDir, name, true)
		}
		if _, readErr := os.Stat(f.deleteLog); !os.IsNotExist(readErr) {
			t.Fatalf("remote deletion occurred after declined confirmation: %v", readErr)
		}
	})

	t.Run("declined Apple-state confirmation leaves everything untouched", func(t *testing.T) {
		f := newResetTestFixture(t)
		f.seedState(t)

		output, err := f.runPTY(t, []struct {
			prompt string
			reply  string
		}{
			{prompt: "Type 'reset' to confirm:", reply: "reset"},
			{prompt: "Type 'DELETE IMESSAGE STATE' to confirm:", reply: "no"},
		}, "--delete-imessage-state")
		if err == nil {
			t.Fatalf("reset unexpectedly succeeded after declined Apple-state confirmation\n%s", output)
		}
		if !strings.Contains(string(output), "Type 'DELETE IMESSAGE STATE' to confirm:") {
			t.Fatalf("reset did not reach the Apple-state confirmation prompt:\n%s", output)
		}
		for _, name := range []string{
			"config.yaml",
			"corten-matrix.db",
			"session.json",
			"keystore.plist",
			"trustedpeers.plist",
			"future-apple-state.sentinel",
			"state",
			"anisette",
		} {
			assertResetPathExists(t, f.stateDir, name, true)
		}
		if _, readErr := os.Stat(f.deleteLog); !os.IsNotExist(readErr) {
			t.Fatalf("remote deletion occurred after declined Apple-state confirmation: %v", readErr)
		}
	})

	t.Run("explicit flag wipes Apple state after confirmation", func(t *testing.T) {
		f := newResetTestFixture(t)
		f.seedState(t)
		t.Setenv("RESET_TEST_RESTORE_FAIL", "1")

		// --yes is the upstream non-interactive confirmation. The separate
		// --delete-imessage-state flag is still required to authorize Apple-state
		// deletion; the default --yes path below preserves it.
		output, err := f.run(t, "", "--yes", "--delete-imessage-state")
		if err != nil {
			t.Fatalf("explicit Apple-state reset failed: %v\n%s", err, output)
		}
		for _, name := range []string{
			"config.yaml",
			"corten-matrix.db",
			"corten-matrix.db-wal",
			"corten-matrix.db-shm",
			"corten-matrix.db-journal",
			"bridge.stdout.log",
			"bridge.stderr.log",
			"logs",
			"session.json",
			"keystore.plist",
			"trustedpeers.plist",
			".preferred-handle",
			"future-apple-state.sentinel",
			"state",
			"anisette",
		} {
			assertResetPathExists(t, f.stateDir, name, false)
		}
	})
}

func TestResetRestoreValidationFailurePreservesRemoteAndLocalState(t *testing.T) {
	f := newResetTestFixture(t)
	f.seedState(t)
	t.Setenv("RESET_TEST_RESTORE_FAIL", "1")

	output, err := f.run(t, "", "--yes")
	if err == nil {
		t.Fatalf("reset unexpectedly succeeded with an unrestorable session\n%s", output)
	}
	if !strings.Contains(string(output), "session state is not safely restorable") {
		t.Fatalf("reset did not report restore validation failure:\n%s", output)
	}
	if !strings.Contains(string(output), "--delete-imessage-state") {
		t.Fatalf("reset omitted the explicit fresh-login escape hatch:\n%s", output)
	}
	for _, name := range []string{
		"config.yaml",
		"corten-matrix.db",
		"session.json",
		"keystore.plist",
		"logs",
	} {
		assertResetPathExists(t, f.stateDir, name, true)
	}
	if _, statErr := os.Stat(f.deleteLog); !os.IsNotExist(statErr) {
		t.Fatalf("remote deletion ran after restore validation failed: %v", statErr)
	}
}

func TestResetRemoteDeleteFailurePreservesLocalState(t *testing.T) {
	f := newResetTestFixture(t)
	f.seedState(t)
	t.Setenv("RESET_TEST_DELETE_FAIL", "1")

	output, err := f.run(t, "", "--yes")
	if err == nil {
		t.Fatalf("reset unexpectedly succeeded after Beeper deletion failed\n%s", output)
	}
	if !strings.Contains(string(output), "registration deletion failed") {
		t.Fatalf("reset did not report Beeper deletion failure:\n%s", output)
	}
	for _, name := range []string{
		"config.yaml",
		"corten-matrix.db",
		"session.json",
		"keystore.plist",
		"logs",
	} {
		assertResetPathExists(t, f.stateDir, name, true)
	}
	deleteArgs, readErr := os.ReadFile(f.deleteLog)
	if readErr != nil {
		t.Fatalf("read fake Beeper delete log: %v", readErr)
	}
	if got := strings.TrimSpace(string(deleteArgs)); got != "sh-imessage" {
		t.Fatalf("failed Beeper delete targeted %q, want sh-imessage", got)
	}
}

func TestResetWhoamiFailurePreservesLocalState(t *testing.T) {
	f := newResetTestFixture(t)
	f.seedState(t)
	t.Setenv("RESET_TEST_WHOAMI_FAIL", "1")

	output, err := f.run(t, "", "--yes")
	if err == nil {
		t.Fatalf("reset unexpectedly succeeded when Beeper verification failed\n%s", output)
	}
	if !strings.Contains(string(output), "Beeper registration preflight failed") {
		t.Fatalf("reset did not report Beeper verification failure:\n%s", output)
	}
	if !strings.Contains(string(output), "self-hosted installs are out of scope") {
		t.Fatalf("reset did not explain its Beeper-only scope:\n%s", output)
	}
	for _, name := range []string{
		"config.yaml",
		"corten-matrix.db",
		"session.json",
		"keystore.plist",
		"logs",
	} {
		assertResetPathExists(t, f.stateDir, name, true)
	}
	if _, statErr := os.Stat(f.deleteLog); !os.IsNotExist(statErr) {
		t.Fatalf("remote deletion ran after Beeper verification failed: %v", statErr)
	}
	if _, statErr := os.Stat(f.serviceLog); !os.IsNotExist(statErr) {
		t.Fatalf("service was stopped after Beeper preflight failed: %v", statErr)
	}
}

func TestResetUsesUpstreamBridgeNameWhenEnvironmentIsUnset(t *testing.T) {
	f := newResetTestFixture(t)
	f.seedState(t)

	if _, err := f.run(t, "", "--yes"); err != nil {
		t.Fatalf("reset failed: %v", err)
	}
	data, err := os.ReadFile(f.deleteLog)
	if err != nil {
		t.Fatalf("read fake Beeper delete log: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "sh-imessage" {
		t.Fatalf("reset deleted registration %q, want sh-imessage", got)
	}
}
