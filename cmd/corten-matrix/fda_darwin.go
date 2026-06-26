//go:build darwin

package main

/*
#include <spawn.h>
#include <stdlib.h>
#include <sys/wait.h>

// Private libSystem API (no public header): makes a spawned child its own TCC
// "responsible" process rather than inheriting ours. Without this, a CLI probe
// launched from Terminal is attributed to Terminal, so the bridge binary itself
// never gets evaluated for Full Disk Access and never appears in the list.
extern int responsibility_spawnattrs_setdisclaim(posix_spawnattr_t *, int);

static int spawn_disclaimed(const char *path, char *const argv[]) {
    posix_spawnattr_t attr;
    if (posix_spawnattr_init(&attr) != 0) return 127;
    responsibility_spawnattrs_setdisclaim(&attr, 1);
    pid_t pid;
    int rc = posix_spawn(&pid, path, NULL, &attr, argv, NULL);
    posix_spawnattr_destroy(&attr);
    if (rc != 0) return 127;
    int status = 0;
    if (waitpid(pid, &status, 0) < 0) return 1;
    return WIFEXITED(status) ? WEXITSTATUS(status) : 1;
}
*/
import "C"

import (
	"os"
	"unsafe"
)

// fdaCheck re-spawns this binary as `corten-matrix fda-probe` with TCC
// responsibility disclaimed, so the probe runs as the bridge binary's own
// responsible process (not Terminal). The disclaimed child's attempt to open
// chat.db is what registers this binary under Full Disk Access. Returns the
// child's exit code (0 = chat.db readable, 1 = not).
func fdaCheck() int {
	self, err := os.Executable()
	if err != nil {
		return 1
	}
	cpath := C.CString(self)
	defer C.free(unsafe.Pointer(cpath))
	arg0 := C.CString(self)
	defer C.free(unsafe.Pointer(arg0))
	arg1 := C.CString("fda-probe")
	defer C.free(unsafe.Pointer(arg1))
	argv := []*C.char{arg0, arg1, nil}
	return int(C.spawn_disclaimed(cpath, &argv[0]))
}
