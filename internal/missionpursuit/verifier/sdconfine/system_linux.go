//go:build linux

package sdconfine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// trustedBinary returns the first candidate that is a regular file owned by root and writable by no one else (the same rule as
// the process-group units' lookup in internal/doomsday/stop).
func trustedBinary(candidates ...string) (string, error) {
	for _, c := range candidates {
		p, err := filepath.EvalSymlinks(c)
		if err != nil {
			continue
		}
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o022 != 0 {
			continue
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != 0 {
			continue
		}
		return p, nil
	}
	return "", fmt.Errorf("none of %v is a root-owned file that others cannot write", candidates)
}

func defaultSystem() system {
	return system{
		goos:   runtime.GOOS,
		uid:    os.Getuid(),
		getenv: os.Getenv,
		trusted: func(name string) (string, error) {
			switch name {
			case "systemd-run":
				return trustedBinary("/usr/bin/systemd-run", "/bin/systemd-run")
			case "systemctl":
				return trustedBinary("/usr/bin/systemctl", "/bin/systemctl")
			}
			return "", fmt.Errorf("%s is not a program this package starts", name)
		},
		checkRuntimeDir: func(dir string) error {
			st, err := os.Lstat(dir)
			if err != nil {
				return err
			}
			if sys, ok := st.Sys().(*syscall.Stat_t); !ok || !st.IsDir() || int(sys.Uid) != os.Getuid() || st.Mode().Perm()&0o077 != 0 {
				return errors.New("is not this user's private folder")
			}
			return nil
		},
		checkBus: func(path string) error {
			st, err := os.Stat(path)
			if err != nil {
				return err
			}
			if st.Mode()&os.ModeSocket == 0 {
				return errors.New("is not a socket")
			}
			return nil
		},
		readFile: os.ReadFile,
		run:      runBounded,
		newID: func() string {
			var b [8]byte
			if _, err := rand.Read(b[:]); err != nil {
				panic("sdconfine: no randomness: " + err.Error())
			}
			return hex.EncodeToString(b[:])
		},
		now:   time.Now,
		sleep: time.Sleep,
	}
}

// capBuffer keeps the first limit bytes and drops the rest.
type capBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// runBounded is the only place this package starts a process. bin is one of the two trusted programs; the environment is exactly
// env (nothing of the host's), and the output is bounded.
func runBounded(ctx context.Context, bin string, env []string, args ...string) runResult {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	so, se := &capBuffer{limit: 1 << 16}, &capBuffer{limit: 1 << 16}
	cmd.Stdout, cmd.Stderr = so, se
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	r := runResult{Stdout: so.buf.String(), Stderr: se.buf.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		r.Code = ee.ExitCode()
	default:
		r.Code, r.Err = -1, err
	}
	return r
}
