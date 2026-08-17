package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeTestAccount(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "config.yaml"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return d
}

func outputFile(t *testing.T, c *exec.Cmd) *os.File {
	t.Helper()
	f, ok := c.Stdout.(*os.File)
	if !ok {
		t.Fatalf("Stdout = %T, want *os.File", c.Stdout)
	}
	if c.Stderr != f {
		t.Fatal("stdout and stderr do not share the account capture file")
	}
	return f
}

func TestAccountOutputReleasedAfterSuccessfulWait(t *testing.T) {
	d := writeTestAccount(t)
	c, release := accountCmdWithOutput("/usr/bin/true", 0, d)
	f := outputFile(t, c)
	if err := c.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	release()
	if _, err := f.Stat(); err == nil {
		t.Fatal("bridge.stdout.log parent descriptor is still open after a successful child")
	}
}

func TestAccountOutputReleasedAfterStartFailure(t *testing.T) {
	d := writeTestAccount(t)
	c, release := accountCmdWithOutput(filepath.Join(d, "does-not-exist"), 0, d)
	f := outputFile(t, c)
	if err := c.Start(); err == nil {
		t.Fatal("Start unexpectedly succeeded")
	}
	release()
	if _, err := f.Stat(); err == nil {
		t.Fatal("bridge.stdout.log parent descriptor is still open after a failed Start")
	}
}

func TestRunAccountProcessStopsAndReapsChild(t *testing.T) {
	d := writeTestAccount(t)
	child := filepath.Join(d, "child.sh")
	startedMarker := filepath.Join(d, "started")
	script := "#!/bin/sh\necho started > " + startedMarker + "\ntrap 'exit 0' TERM INT\nwhile :; do sleep 0.01; done\n"
	if err := os.WriteFile(child, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	c, release := accountCmdWithOutput(child, 0, d)
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		err      error
		started  bool
		stopping bool
	}
	done := make(chan result, 1)
	go func() {
		err, started, stopping := runAccountProcess(ctx, c)
		done <- result{err: err, started: started, stopping: stopping}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(startedMarker); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := os.Stat(startedMarker); err != nil {
		t.Fatal("child never started")
	}
	cancel()
	select {
	case got := <-done:
		if got.err != nil || !got.started || !got.stopping {
			t.Fatalf("runAccountProcess = (%v, %v, %v), want (nil, true, true)", got.err, got.started, got.stopping)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runAccountProcess did not reap the child after cancellation")
	}
	if c.ProcessState == nil {
		t.Fatalf("child process was not reaped: state=%#v", c.ProcessState)
	}
	status, ok := c.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || (!status.Exited() && !status.Signaled()) {
		t.Fatalf("child process was not reaped: state=%#v", c.ProcessState)
	}
}

func TestAccountSupervisorsAreIndependent(t *testing.T) {
	brokenDir := writeTestAccount(t)
	broken := filepath.Join(brokenDir, "broken.sh")
	if err := os.WriteFile(broken, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	healthyDir := writeTestAccount(t)
	startedMarker := filepath.Join(healthyDir, "started")
	stoppedMarker := filepath.Join(healthyDir, "stopped")
	healthy := filepath.Join(healthyDir, "healthy.sh")
	script := "#!/bin/sh\necho started > " + startedMarker + "\ntrap 'echo stopped > " + stoppedMarker + "; exit 0' TERM INT\nwhile :; do sleep 0.01; done\n"
	if err := os.WriteFile(healthy, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	brokenCtx, cancelBroken := context.WithCancel(context.Background())
	healthyCtx, cancelHealthy := context.WithCancel(context.Background())
	defer cancelBroken()
	defer cancelHealthy()
	brokenDone := make(chan struct{})
	healthyDone := make(chan struct{})
	go func() {
		superviseAccountContextWithDelay(brokenCtx, broken, 0, brokenDir, time.Millisecond)
		close(brokenDone)
	}()
	go func() {
		superviseAccountContextWithDelay(healthyCtx, healthy, 1, healthyDir, time.Millisecond)
		close(healthyDone)
	}()

	waitForFile := func(path string) bool {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(path); err == nil {
				return true
			}
			time.Sleep(time.Millisecond)
		}
		return false
	}
	if !waitForFile(startedMarker) {
		t.Fatal("healthy account never started")
	}
	cancelBroken()
	select {
	case <-brokenDone:
	case <-time.After(2 * time.Second):
		t.Fatal("broken account supervisor did not stop")
	}
	if _, err := os.Stat(stoppedMarker); err == nil {
		t.Fatal("stopping the broken account affected the healthy account")
	}

	cancelHealthy()
	select {
	case <-healthyDone:
	case <-time.After(2 * time.Second):
		t.Fatal("healthy account supervisor did not stop")
	}
	if !waitForFile(stoppedMarker) {
		t.Fatal("healthy child did not receive a shutdown signal")
	}
}

func TestSafeStartErrorDoesNotExposeDataDirectory(t *testing.T) {
	secretDir := filepath.Join(t.TempDir(), "private-account-data")
	err := &os.PathError{Op: "chdir", Path: secretDir, Err: syscall.ENOENT}
	got := safeStartError(err)
	if strings.Contains(got, secretDir) {
		t.Fatalf("safeStartError leaked %q: %s", secretDir, got)
	}
	if got != "not found" {
		t.Errorf("safeStartError = %q, want the path-free errno summary", got)
	}
	if safeStartError(errors.New("unexpected internal failure")) == "" {
		t.Error("safeStartError returned an empty diagnostic")
	}
}
