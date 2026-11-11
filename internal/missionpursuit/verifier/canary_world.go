package verifier

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
)

// canaryWorld is what the host plants before the canaries run, and takes away after: a file and a free path in the parent of the
// scratch base (C1, C2), a listening unix socket in the runtime dir and in the temp dir (C3), a marker file in the person's home
// beside whatever secret folders exist there (C4), a process whose environment holds a secret and the variable in the host's own
// environment (C5), and the host's namespace links (C6). Everything it makes carries "canary" and a random name, and nothing in
// it is a real secret.
type canaryWorld struct {
	file, outside string
	sockets       []string
	secrets       []string
	nsLinks       []string
	secretPID     int
	secretVar     string
	shmMarker     string
	shmProbe      string
	ptySlaves     []string
	errs          map[string]error
	cleanup       []func()
}

const canarySecretPrefix = "MONOAGENT_CANARY_SECRET_"

// staleAfter is how old a plant must be before the next run removes it as the leftover of a run that crashed.
const staleAfter = time.Hour

func (w *canaryWorld) err(id string) error { return w.errs[id] }

func (w *canaryWorld) close() {
	for i := len(w.cleanup) - 1; i >= 0; i-- {
		w.cleanup[i]()
	}
}

func randHex() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("verifier: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// plantable is a path that fits inside the single quotes of a canary spec and in a shell word.
func plantable(p string) error {
	if p == "" || !filepath.IsAbs(p) || strings.ContainsAny(p, "'\x00\n\r") {
		return fmt.Errorf("the path %q cannot be named in a canary", p)
	}
	return nil
}

func newCanaryWorld(ctx context.Context, scratchBase string, o CanaryOptions, conf Confiner) *canaryWorld {
	w := &canaryWorld{errs: map[string]error{}}
	parent := filepath.Dir(scratchBase)
	home, runtimeDir, tmp := o.HomeDir, o.RuntimeDir, o.TmpDir
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if runtimeDir == "" {
		runtimeDir = os.Getenv("XDG_RUNTIME_DIR")
	}
	if tmp == "" {
		tmp = os.TempDir()
	}
	shm := o.ShmDir
	if shm == "" {
		shm = "/dev/shm"
	}
	sweepStale(scratchBase, parent, home, runtimeDir, tmp, shm)

	// C1, C2: in the parent of the scratch base, which a confiner that confines nothing leaves readable and writable.
	if err := plantable(parent); err != nil {
		w.errs["C1"], w.errs["C2"] = err, err
	} else if f, err := os.CreateTemp(parent, ".canary-r-*"); err != nil {
		w.errs["C1"] = err
	} else {
		f.WriteString("canary file outside the work area\n")
		f.Close()
		w.file = f.Name()
		w.cleanup = append(w.cleanup, func() { os.Remove(f.Name()) })
		if err := plantable(w.file); err != nil {
			w.errs["C1"] = err
		}
	}
	if w.errs["C2"] == nil {
		w.outside = filepath.Join(parent, ".canary-w-"+randHex())
		w.cleanup = append(w.cleanup, func() { os.Remove(w.outside) })
	}

	// C3: a real listening socket where host services keep theirs.
	for _, dir := range []string{runtimeDir, tmp} {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, "monoagent-canary-"+randHex()+".sock")
		if err := plantable(p); err != nil {
			w.errs["C3"] = err
			continue
		}
		closeSock, err := listenUnix(p)
		if err != nil {
			w.errs["C3"] = fmt.Errorf("a socket in %s: %w", dir, err)
			continue
		}
		w.cleanup = append(w.cleanup, closeSock)
		w.sockets = append(w.sockets, p)
	}
	if len(w.sockets) == 0 && w.errs["C3"] == nil {
		w.errs["C3"] = errors.New("no place to plant a socket")
	}

	// C4: a marker in the home, and the secret folders that exist there (a path that does not exist proves nothing).
	if err := plantable(home); err != nil {
		w.errs["C4"] = err
	} else {
		m := filepath.Join(home, ".canary-h-"+randHex())
		if err := os.WriteFile(m, []byte("canary marker\n"), 0o600); err != nil {
			w.errs["C4"] = err
		} else {
			w.cleanup = append(w.cleanup, func() { os.Remove(m) })
			w.secrets = append(w.secrets, m)
		}
		cand := []string{filepath.Join(home, ".ssh"), filepath.Join(home, ".monoagent"), filepath.Join(home, ".config")}
		if runtimeDir != "" {
			cand = append(cand, filepath.Join(runtimeDir, "bus"))
		}
		for _, p := range cand {
			if _, err := os.Lstat(p); err == nil && plantable(p) == nil {
				w.secrets = append(w.secrets, p)
			}
		}
	}

	// C5: the host's own environment and a process that holds the secret in its environment.
	w.plantSecretProcess(ctx)

	// C12: a variable in the manager that starts the programs, where the confiner has one; taken out again by name.
	if pl, ok := conf.(ManagerEnvPlanter); ok {
		name := "MONOAGENT_CANARY_MGR_" + randHex()
		if undo, err := pl.PlantManagerEnv(ctx, name, randHex()); err != nil {
			w.errs["C12"] = fmt.Errorf("the manager's environment could not be planted: %w", err)
		} else {
			w.cleanup = append(w.cleanup, undo)
		}
	}

	// C13: a marker in the host's /dev/shm, and a path the program writes to there (it must not exist on the host afterwards).
	if st, err := os.Stat(shm); err != nil || !st.IsDir() {
		w.errs["C13"] = fmt.Errorf("no shared-memory folder at %s to plant in", shm)
	} else {
		w.shmMarker = filepath.Join(shm, "monoagent-canary-"+randHex())
		w.shmProbe = filepath.Join(shm, "monoagent-canary-w-"+randHex())
		if err := plantable(w.shmMarker); err != nil {
			w.errs["C13"] = err
		} else if err := os.WriteFile(w.shmMarker, []byte("canary marker\n"), 0o600); err != nil {
			w.errs["C13"] = err
		} else {
			w.cleanup = append(w.cleanup, func() { os.Remove(w.shmMarker); os.Remove(w.shmProbe) })
		}
	}

	// C14: a terminal of the host's, made by the host for the occasion (never a real one of the person's), and whatever else is
	// in /dev/pts: a program must see none.
	if slave, closePty, err := plantPty(); err == nil {
		w.cleanup = append(w.cleanup, closePty)
		w.ptySlaves = append(w.ptySlaves, slave)
	}

	// C6: the host's namespaces.
	for _, n := range []string{"net", "pid", "mnt", "ipc", "user"} {
		link, err := os.Readlink("/proc/self/ns/" + n)
		if err != nil || strings.ContainsAny(link, "'\n") {
			w.errs["C6"] = fmt.Errorf("the host's %s namespace cannot be read: %v", n, err)
			break
		}
		w.nsLinks = append(w.nsLinks, n+"="+link)
	}
	return w
}

func (w *canaryWorld) plantSecretProcess(ctx context.Context) {
	secret := randHex() + randHex()
	w.secretVar = canarySecretPrefix + randHex() // per run: overlapping runs in one process never see or remove each other's
	os.Setenv(w.secretVar, secret)
	w.cleanup = append(w.cleanup, func() { os.Unsetenv(w.secretVar) })
	sleepBin := ""
	for _, d := range filepath.SplitList(finish.SystemPath) {
		if st, err := os.Stat(filepath.Join(d, "sleep")); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			sleepBin = filepath.Join(d, "sleep")
			break
		}
	}
	if sleepBin == "" {
		w.errs["C5"] = errors.New("no sleep on the system path to hold the secret")
		return
	}
	cmd := exec.CommandContext(ctx, sleepBin, "120")
	cmd.Env = append(os.Environ(), w.secretVar+"="+secret)
	if err := cmd.Start(); err != nil {
		w.errs["C5"] = err
		return
	}
	w.cleanup = append(w.cleanup, func() { cmd.Process.Kill(); cmd.Wait() })
	w.secretPID = cmd.Process.Pid
	// the kernel shows the new environment a moment after the exec; wait for it
	var b []byte
	var err error
	for i := 0; i < 100; i++ {
		if b, err = os.ReadFile(fmt.Sprintf("/proc/%d/environ", w.secretPID)); err == nil && strings.Contains(string(b), w.secretVar+"="+secret) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	w.errs["C5"] = fmt.Errorf("the host cannot read its own planted process's environment (%v), so the canary would prove nothing", err)
}

// sweepStale removes what a run that crashed left: plants older than an hour. A fresh one belongs to a run in progress.
func sweepStale(scratchBase, parent, home, runtimeDir, tmp, shm string) {
	cutoff := time.Now().Add(-staleAfter)
	sweep := func(dir string, match func(name string) bool) {
		if dir == "" {
			return
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			if !match(e.Name()) {
				continue
			}
			if info, err := e.Info(); err != nil || info.ModTime().After(cutoff) {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if e.IsDir() {
				finish.RemoveTree(p)
			} else {
				os.Remove(p)
			}
		}
	}
	has := func(prefix string) func(string) bool {
		return func(n string) bool { return strings.HasPrefix(n, prefix) }
	}
	sweep(scratchBase, has("canary-"))
	sweep(parent, func(n string) bool { return strings.HasPrefix(n, ".canary-r-") || strings.HasPrefix(n, ".canary-w-") })
	sweep(home, has(".canary-h-"))
	for _, d := range []string{runtimeDir, tmp, shm} {
		sweep(d, has("monoagent-canary-"))
	}
}
