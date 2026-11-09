//go:build linux

package verifier

import (
	"fmt"
	"os"
	"strings"
)

// readState is the process state letter, or an error when the process is gone.
func readState(pid int) (byte, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 || i+2 >= len(b) {
		return 0, fmt.Errorf("odd stat")
	}
	return b[i+2], nil
}
