//go:build linux

package agentbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// canaryProbe runs inside the box: $1 state directory, $2 nonce, $3 mode, $4 the number of
// bound files expected inside the state directory, then those files, then the masked
// targets that must be empty.
const canaryProbe = `
fail() { echo "canary: $1"; exit 1; }
state=$1; nonce=$2; mode=$3; n=$4; shift 4
allowed=""; i=0
while [ "$i" -lt "$n" ]; do allowed="$allowed$1
"; shift; i=$((i + 1)); done
unexpected=$(find "$state" -mindepth 1 ! -type d 2>/dev/null | grep -vxF "$allowed")
if [ -n "$unexpected" ]; then fail "the state directory is readable"; fi
if [ -n "${SSH_AUTH_SOCK+x}" ]; then fail "SSH_AUTH_SOCK is set"; fi
for m in "$@"; do
  if [ -d "$m" ] && [ -n "$(ls -A "$m" 2>/dev/null)" ]; then fail "$m is readable"; fi
  if [ -f "$m" ] && [ -s "$m" ]; then fail "$m is readable"; fi
done
n=$(ls /proc | grep -c '^[0-9][0-9]*$')
if [ "$n" -gt 12 ]; then fail "host processes are visible ($n)"; fi
if [ "$mode" = strict ]; then
  : > "$HOME/.monoagent-canary-$nonce" 2>/dev/null || fail "the home directory is not writable"
elif [ -e "$HOME/.bashrc" ] && ( : >> "$HOME/.bashrc" ) 2>/dev/null; then
  fail "the shell startup file is writable"
fi
echo canary-ok
`

// canaryPlan is the command that runs the canary in the box configuration of the turn: the
// same mounts, with the probe as the payload. marker is a file that must not appear on the
// host.
func (b Box) canaryPlan() (plan Plan, marker string, err error) {
	home, err := b.home()
	if err != nil {
		return Plan{}, "", errf(CodeUnavailable, "home directory: %v", err)
	}
	_, allMasks, err := b.forbiddenPaths(home, "")
	if err != nil {
		return Plan{}, "", err
	}
	state := b.stateDir(home)
	var masks []string // the state directory itself is checked file by file below
	for _, m := range allMasks {
		if m != state && m != mountPoint(state) {
			masks = append(masks, m)
		}
	}
	nb := make([]byte, 8)
	if _, err := rand.Read(nb); err != nil {
		return Plan{}, "", err
	}
	nonce := hex.EncodeToString(nb)
	var inState []string
	for _, f := range append(append([]string(nil), b.ReadOnly...), b.Attach...) {
		if r := mountPoint(f); strings.HasPrefix(r, mountPoint(state)+"/") || strings.HasPrefix(f, state+"/") {
			inState = append(inState, f)
		}
	}
	args := append([]string{"-c", canaryProbe, "sh", state, nonce, string(b.Mode), fmt.Sprint(len(inState))}, inState...)
	args = append(args, masks...)
	plan, err = b.Wrap("sh", args, os.Environ())
	return plan, filepath.Join(home, ".monoagent-canary-"+nonce), err
}

// Canary runs a short check in the same box configuration as the turn and returns an error
// when the box does not hold: the state directory readable, SSH_AUTH_SOCK passed on, host
// processes visible, or (strict) a write to the home directory reaching the host.
func (b Box) Canary(ctx context.Context) error {
	plan, marker, err := b.canaryPlan()
	if err != nil {
		return err
	}
	defer plan.Close()
	return runCanary(ctx, plan, marker)
}

func runCanary(ctx context.Context, plan Plan, marker string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	defer os.Remove(marker) // only ever present if the box leaked
	cmd := plan.Command()
	out, err := runWithContext(ctx, cmd)
	if _, serr := os.Stat(marker); serr == nil {
		return errf(CodeCanary, "the sandbox canary failed: a write to the home directory reached the host")
	}
	if err != nil || !strings.Contains(string(out), "canary-ok") {
		return errf(CodeCanary, "the sandbox canary failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func runWithContext(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return buf.Bytes(), err
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return buf.Bytes(), ctx.Err()
	}
}
