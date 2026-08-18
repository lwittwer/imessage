package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lrhodin/corten-matrix/scripts"
)

const resetTestBundleID = "com.example.corten-matrix-test"

type resetTestFixture struct {
	root      string
	stateDir  string
	fakeBin   string
	binary    string
	deleteLog string
	script    string
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
	fakeBinary := filepath.Join(fakeBin, "corten-matrix")
	writeResetTestExecutable(t, fakeBinary, `#!/bin/sh
set -eu
case "${1:-} ${2:-}" in
  "bbctl whoami")
    printf '%s\n' 'test-user' '  sh-imessage imessage RUNNING'
    ;;
  "bbctl delete")
    printf '%s\n' "${3:-}" >> "$RESET_TEST_DELETE_LOG"
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
		"uname":      "#!/bin/sh\nprintf '%s\\n' Linux\n",
		"systemctl":  "#!/bin/sh\nexit 0\n",
		"journalctl": "#!/bin/sh\nexit 0\n",
		"pgrep":      "#!/bin/sh\nexit 1\n",
		"sleep":      "#!/bin/sh\nexit 0\n",
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
		root:      root,
		stateDir:  stateDir,
		fakeBin:   fakeBin,
		binary:    fakeBinary,
		deleteLog: deleteLog,
		script:    scriptPath,
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
		"config.yaml",
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
	for _, name := range []string{
		"logs/bridge.log",
		"state/apple-identity.bin",
		"anisette/device-state.bin",
	} {
		f.writeFile(t, name)
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
	cmd.Env = append(filteredResetTestEnvironment(),
		"HOME="+f.root,
		"XDG_DATA_HOME="+filepath.Dir(f.stateDir),
		"PATH="+f.fakeBin+":/usr/bin:/bin",
		"RESET_TEST_DELETE_LOG="+f.deleteLog,
		"BRIDGE_NAME=",
	)
	cmd.Stdin = strings.NewReader(input)
	return cmd.CombinedOutput()
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
		"config.yaml.bak.20260818120000",
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
}

func TestResetDeleteAppleStateRequiresExplicitConfirmation(t *testing.T) {
	t.Run("declined confirmation leaves everything untouched", func(t *testing.T) {
		f := newResetTestFixture(t)
		f.seedState(t)

		output, err := f.run(t, "no\n", "--delete-imessage-state")
		if err == nil {
			t.Fatalf("reset unexpectedly succeeded after declined confirmation\n%s", output)
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

	t.Run("explicit flag wipes Apple state after confirmation", func(t *testing.T) {
		f := newResetTestFixture(t)
		f.seedState(t)

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
