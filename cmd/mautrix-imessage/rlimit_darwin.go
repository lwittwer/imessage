package main

import (
	"fmt"
	"os"
	"syscall"
)

// desiredFileLimit is the open-file soft limit we want the bridge to run with
// on macOS. 65536 is comfortably below this machine class's per-process kernel
// maximum (kern.maxfilesperproc, typically 184320) while giving a long-running
// bridge enormous headroom over launchd's default.
const desiredFileLimit = 65536

// raiseFileLimit raises the process's open-file-descriptor soft limit on macOS.
//
// launchd starts agents/daemons with a soft RLIMIT_NOFILE of only 256. A
// long-running bridge keeps many descriptors open at once — the APNs
// connection, IDS HTTP requests, the appservice websocket (which the homeserver
// also tunnels HTTP through), the SQLite pool, CardDAV — and over a day or two
// of uptime can approach 256. Once the limit is hit, every new socket fails
// with "too many open files": the appservice websocket cannot reopen after its
// (normal) hourly server-side recycle, and APNs cannot re-establish, so the
// bridge silently stops passing messages until it is restarted (which resets
// the count). This is macOS-specific because Linux/systemd installs start with
// a far higher limit and never reach it.
//
// We try to raise the soft limit toward a sane ceiling. macOS's getrlimit often
// reports an effectively-infinite hard limit, but setrlimit rejects a soft limit
// above the kernel's per-process maximum, so we fall back to the historical
// OPEN_MAX (10240) — still a 40x improvement over 256 — if the higher target is
// refused. Best-effort: on any failure we log and continue rather than abort.
func raiseFileLimit() {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		fmt.Fprintf(os.Stderr, "[rlimit] failed to read RLIMIT_NOFILE: %v\n", err)
		return
	}
	if lim.Cur >= desiredFileLimit {
		// Already raised (operator override, or launchd plist limits applied).
		return
	}
	old := lim.Cur
	for _, target := range []uint64{desiredFileLimit, 10240} {
		if target <= old {
			continue
		}
		if lim.Max != 0 && target > lim.Max {
			continue
		}
		lim.Cur = target
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &lim); err == nil {
			fmt.Fprintf(os.Stderr, "[rlimit] raised open-file limit %d → %d\n", old, target)
			return
		}
	}
	fmt.Fprintf(os.Stderr, "[rlimit] could not raise open-file limit above %d (launchd default is 256); "+
		"the bridge may exhaust descriptors over long uptime\n", old)
}
