//go:build !windows

package main

import (
	"os"
	"syscall"
)

// cliOverrideTrusted reports whether the MONOAGENTCLI_BIN file may be run: a
// regular file owned by the current user and not group/world writable.
func cliOverrideTrusted(p string) bool {
	info, err := os.Stat(p)
	if err != nil || info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
