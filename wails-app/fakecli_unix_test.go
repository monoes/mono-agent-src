//go:build !windows

package main

import "os"

// fakeCLIName is the file name tests give a fake monoagentcli.
const fakeCLIName = "monoagentcli"

// writeFakeCLI writes script (a shell script) as the executable path.
func writeFakeCLI(path, script string) error {
	return os.WriteFile(path, []byte(script), 0o755)
}

// fakeCLIShimMain is the Windows shim entry (see fakecli_windows_test.go); never handled here.
func fakeCLIShimMain() bool { return false }
