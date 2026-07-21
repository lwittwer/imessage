package cli

import (
	"strings"
	"testing"

	"github.com/lrhodin/corten-matrix/scripts"
)

func embeddedScript(t *testing.T, name string) string {
	t.Helper()
	data, err := scripts.Files.ReadFile(name)
	if err != nil {
		t.Fatalf("read embedded script %s: %v", name, err)
	}
	return string(data)
}

func TestResetRequiresConfirmationBeforeMutation(t *testing.T) {
	script := embeddedScript(t, "reset-bridge.sh")
	stop := strings.Index(script, "echo \"Stopping bridge...\"")
	localConfirm := strings.Index(script, `read -r -p "Type RESET LOCAL STATE to continue: "`)
	remoteConfirm := strings.Index(script, `read -r -p "Type DELETE MATRIX ROOMS to confirm remote deletion: "`)
	deleteLocal := strings.Index(script, "rm -rf -- \"$dir\"")

	if stop < 0 || localConfirm < 0 || remoteConfirm < 0 || deleteLocal < 0 {
		t.Fatalf("reset script is missing a required confirmation or mutation marker")
	}
	if localConfirm > stop || remoteConfirm > stop {
		t.Fatalf("reset can stop the service before all confirmations: local=%d remote=%d stop=%d", localConfirm, remoteConfirm, stop)
	}
	if stop > deleteLocal {
		t.Fatalf("local state deletion appears before the service stop")
	}
	for _, required := range []string{
		"if [ ! -t 0 ]",
		"--delete-remote",
		"--external-database-cleared",
		"Local file deletion cannot clear that external database.",
		"refusing unsafe reset target",
		"a corten-matrix bridge process is still running; no state was deleted.",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("reset script missing safety guard %q", required)
		}
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
			} {
				if !strings.Contains(script, required) {
					t.Errorf("setup missing safety warning %q", required)
				}
			}
		})
	}
}
