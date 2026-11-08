//go:build linux || darwin

package chat

import (
	"errors"
	"os"
	"os/exec"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// attachPipe gives the child the read end on descriptor 3 (close-on-exec on both ends, so only ExtraFiles hands it on) and
// the marker that names it.
func attachPipe(cmd *exec.Cmd, r, _ *os.File) error {
	if len(cmd.ExtraFiles) != 0 {
		return errors.New("extra files already set")
	}
	cmd.ExtraFiles = []*os.File{r}
	cmd.Env = append(cmd.Env, coderhost.CredFDEnv+"=3")
	return nil
}
