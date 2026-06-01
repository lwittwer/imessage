//go:build !darwin

package main

// raiseFileLimit is a no-op on non-macOS platforms. The descriptor-exhaustion
// problem it addresses is specific to launchd's low default RLIMIT_NOFILE (256)
// on macOS; Linux/systemd installs start with a far higher limit, so the bridge
// leaves their resource limits untouched.
func raiseFileLimit() {}
