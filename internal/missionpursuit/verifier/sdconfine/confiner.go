// Package sdconfine is a verifier.Confiner built on systemd: every program a goal check runs is one transient `systemd-run
// --user` SERVICE with no network, a read-only root, an empty /run, a private /tmp, no /home, a private pid, ipc and user
// namespace, no new privileges, no capabilities, a system-call filter, resource caps and a cgroup kill. It is a library: it wires
// no host, adds no flag and changes no row. Where a piece is missing (not Linux, no systemd-run, no user manager, no delegated
// controller, a property the manager does not know) New returns a typed *Unavailable naming it and the verifier keeps
// "no-confinement".
//
// A unit is not a scope: a scope takes only resource-control properties, the sandboxing properties are settings of services.
//
// What it trusts: nothing the lead wrote. The programs started are the two trusted binaries (root-owned, found only in /usr/bin
// and /bin) with an environment of exactly XDG_RUNTIME_DIR and DBUS_SESSION_BUS_ADDRESS, so no host secret reaches a unit through
// the client. The criterion's own environment (built from nothing by the finish sandbox) is passed with --setenv.
//
// One Confiner serves one Checker at a time: the record of unit failures (TakeInfra) is the confiner's, so two Checkers sharing
// one would read each other's. (A Checker runs one check at a time, and the host has one Checker.) Programs that need a
// pseudo-terminal get none. /dev/hugepages, where the host has a hugetlbfs mount, is not hidden (a missing directory in the private
// /dev would stop the unit from starting; the mount is empty to a program that has no reserved pages).
//
// The only path writable in a unit is the check's working area. The live project is never mounted: criteria run on the
// sandbox's copy; Hide adds the live folder to the inaccessible paths for the places the filesystem rules do not hide anyway.
package sdconfine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
)

// Config builds a Confiner.
type Config struct {
	// ScratchBase is the verifier's scratch folder (the same one): required, an existing safe path. The canaries make their
	// working area in it and the probe unit binds it.
	ScratchBase string
	// Caps can only lower the design's caps (2 GiB, 512 tasks, 200% CPU); the real tests use tiny ones.
	Caps Caps
}

// heavyEvery is how often the heavy canaries (C8 to C11) run again: a cached proof, keyed on the property list and the systemd
// version, covers the self tests in between.
const (
	heavyEvery         = 10 * time.Minute
	lightSelfTestLimit = 2 * time.Minute
	heavySelfTestLimit = 8 * time.Minute
)

type proofState struct {
	mu  sync.Mutex
	key string
	at  time.Time
	// run serializes self tests (and so their units): a second one would read the first's units as left behind.
	run sync.Mutex
}

// infraState collects what the confinement itself got wrong (see verifier.InfraReporter); shared by a confiner and its copies.
type infraState struct {
	mu   sync.Mutex
	list []string
}

// Confiner implements verifier.Confiner, verifier.Hider and verifier.LeftoverChecker.
type Confiner struct {
	sys         system
	cfg         Config
	caps        Caps
	version     int
	runBin      string // the trusted systemd-run
	ctlBin      string // the trusted systemctl
	env         []string
	hide        string
	proof       *proofState
	infra       *infraState
	envBin      string // the trusted env, which starts every program with an empty environment
	owner       string // <pid>-<start time> of this process: in every unit name, so a sweep knows whose units are stale
	privatePIDs bool

	logf     func(string, ...any)
	canaries func(context.Context, verifier.Confiner, string, verifier.CanaryOptions) []verifier.CanaryResult

	// test seams, nil or false in production: propsFn replaces the builder (the self-check must refuse what it drops), mutate
	// alters the list AFTER the self-check (a builder bug), noRelease skips the release.
	propsFn    func(Spec) []string
	mutate     func([]string) []string
	mutateArgv func([]string) []string
	noRelease  bool
}

var (
	_ verifier.Confiner          = (*Confiner)(nil)
	_ verifier.Hider             = (*Confiner)(nil)
	_ verifier.InfraReporter     = (*Confiner)(nil)
	_ verifier.ManagerEnvPlanter = (*Confiner)(nil)
	_ verifier.LeftoverChecker   = (*Confiner)(nil)
)

// New builds the confiner, or returns a typed *Unavailable (and no confiner) naming the piece that is missing. It starts one
// probe unit that carries the full property list and reads back what the manager applied.
func New(ctx context.Context, cfg Config) (*Confiner, error) {
	return newWith(ctx, cfg, defaultSystem())
}

// probeLimits are the limits of the probe unit (and of the key the heavy proof is cached under).
func probeLimits() finish.Limits {
	return finish.Limits{CPUSeconds: 30, AddressSpace: 8 << 30, OpenFiles: 256, FileSize: 1 << 20, Procs: 1}
}

func newWith(ctx context.Context, cfg Config, sys system) (*Confiner, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if sys.goos != "linux" {
		return nil, unavailable(MissingPlatform, "systemd-run confinement exists on Linux only, not %s", sys.goos)
	}
	if err := SafePath(cfg.ScratchBase); err != nil {
		return nil, fmt.Errorf("sdconfine: the scratch folder: %w", err)
	}
	if st, err := os.Stat(cfg.ScratchBase); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("sdconfine: the scratch folder %s is not an existing folder", cfg.ScratchBase)
	}
	c := &Confiner{sys: sys, cfg: cfg, caps: cfg.Caps.normalized(), proof: &proofState{}, infra: &infraState{}, logf: log.Printf, canaries: verifier.RunCanariesWith}
	var err error
	if c.runBin, err = sys.trusted("systemd-run"); err != nil {
		return nil, unavailable(MissingSystemdRun, "%v", err)
	}
	if c.ctlBin, err = sys.trusted("systemctl"); err != nil {
		return nil, unavailable(MissingSystemctl, "%v", err)
	}
	if c.envBin, err = sys.trusted("env"); err != nil {
		return nil, unavailable(MissingEnv, "%v", err)
	}
	start, err := sys.procStart(sys.pid)
	if err != nil || start == "" {
		return nil, fmt.Errorf("sdconfine: this process's start time cannot be read: %v", err)
	}
	c.owner = fmt.Sprintf("%d-%s", sys.pid, start)
	dir := sys.getenv("XDG_RUNTIME_DIR")
	if dir == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || SafePath(dir) != nil {
		return nil, unavailable(MissingUserManager, "XDG_RUNTIME_DIR is not set to an absolute path (there is no user session here)")
	}
	if err := sys.checkRuntimeDir(dir); err != nil {
		return nil, unavailable(MissingUserManager, "%s %v", dir, err)
	}
	if err := sys.checkBus(dir + "/bus"); err != nil {
		return nil, unavailable(MissingUserManager, "the session bus %s/bus: %v", dir, err)
	}
	c.env = []string{"XDG_RUNTIME_DIR=" + dir, "DBUS_SESSION_BUS_ADDRESS=unix:path=" + dir + "/bus"}

	r := c.ctl(ctx, "show", "--property=Version", "--value")
	if r.Code != 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, unavailable(MissingUserManager, "the user manager does not answer: %s", firstLine(r.Stderr, r.Err))
	}
	v := strings.TrimSpace(r.Stdout)
	if i := strings.IndexFunc(v, func(r rune) bool { return r < '0' || r > '9' }); i > 0 {
		v = v[:i]
	}
	if c.version, err = strconv.Atoi(v); err != nil || c.version <= 0 {
		return nil, unavailable(MissingUserManager, "the user manager's version %q is not a number", strings.TrimSpace(r.Stdout))
	}
	if c.version < 256 {
		return nil, unavailable(MissingSystemdVersion, "systemd %d is too old: needs 256 for PrivatePIDs (the pid namespace that hides the host's processes; canary C6 cannot pass without it)", c.version)
	}
	c.privatePIDs = true
	if err := c.probe(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func firstLine(s string, err error) string {
	s = strings.TrimSpace(s)
	if s == "" && err != nil {
		s = err.Error()
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// probe runs one unit with the full property list and reads back the caps the manager applied: a controller that is not
// delegated leaves a cap silently unapplied, which is the one thing a property list cannot show.
func (c *Confiner) probe(ctx context.Context) error {
	spec := Spec{Work: c.cfg.ScratchBase, Lim: probeLimits(), Caps: c.caps, PrivatePIDs: c.privatePIDs}
	unit := c.unitName(c.sys.newID())
	defer c.release(unit)()
	script := `c=/sys/fs/cgroup$(cut -d: -f3 /proc/self/cgroup); echo memory=$(cat $c/memory.max); echo pids=$(cat $c/pids.max); echo cpu=$(cat $c/cpu.max)`
	args, err := c.argv(spec, unit, []string{"PATH=/usr/bin:/bin"}, c.cfg.ScratchBase, "/usr/bin/sh", []string{"-c", script})
	if err != nil {
		return err
	}
	r := c.sys.run(ctx, c.runBin, c.env, args...)
	if err := ctx.Err(); err != nil {
		return err // the caller's cancellation is not "the manager refused the probe"
	}
	if r.Code != 0 {
		return probeRefusal(r)
	}
	got := map[string]string{}
	for _, l := range strings.Split(r.Stdout, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
			got[k] = v
		}
	}
	if got["memory"] != fmt.Sprint(c.caps.MemoryBytes) {
		return unavailable(MissingController, "memory: the manager applied %q, wanted %d (the memory controller is not delegated to the user manager)", got["memory"], c.caps.MemoryBytes)
	}
	if got["pids"] != fmt.Sprint(c.caps.Tasks) {
		return unavailable(MissingController, "pids: the manager applied %q, wanted %d (the pids controller is not delegated to the user manager)", got["pids"], c.caps.Tasks)
	}
	if f := strings.Fields(got["cpu"]); len(f) == 0 || f[0] != fmt.Sprint(c.caps.CPUPercent*1000) {
		return unavailable(MissingController, "cpu: the manager applied %q, wanted a quota of %d%% (the cpu controller is not delegated to the user manager)", got["cpu"], c.caps.CPUPercent)
	}
	return nil
}

// probeRefusal reads systemd-run's complaint: a property it does not know or accept is named; anything else is the manager.
func probeRefusal(r runResult) error {
	line := firstLine(r.Stderr, r.Err)
	if i := strings.Index(line, "Unknown assignment: "); i >= 0 {
		name, _, _ := strings.Cut(line[i+len("Unknown assignment: "):], "=")
		return unavailable(MissingProperty, "the user manager does not know %s (%s)", strings.TrimSpace(name), line)
	}
	if i := strings.Index(line, "Invalid "); i >= 0 {
		if name, _, ok := strings.Cut(line[i+len("Invalid "):], " setting"); ok {
			return unavailable(MissingProperty, "the user manager does not accept %s (%s)", strings.TrimSpace(name), line)
		}
	}
	return unavailable(MissingUserManager, "the probe unit was refused: %s", line)
}

// Name is recorded in every result.
func (c *Confiner) Name() string { return "systemd-run" }

// Hide returns a copy of the confiner for one check that also makes the live project folder inaccessible. The original is
// unchanged; the copy shares the heavy-canary proof. An unsafe root makes Place refuse (never an ignored root).
func (c *Confiner) Hide(root string) verifier.Confiner {
	h := *c
	h.hide = root
	return &h
}

// SelfTest runs the canaries through this confiner. The light ones (C0 to C7) run every time, the heavy ones (C8 to C11: a fork
// bomb, a memory hog, an outliving child, a timeout) at the first call and then at most every ten minutes, on a proof keyed on
// the property list and the systemd version. Any canary that does not hold fails the self test and says which; a failure never
// leaves a cached proof.
func (c *Confiner) SelfTest(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.proof.run.Lock()
	defer c.proof.run.Unlock()
	key := c.proofKey()
	c.proof.mu.Lock()
	heavy := c.proof.key != key || c.sys.now().Sub(c.proof.at) >= heavyEvery
	c.proof.mu.Unlock()
	limit := lightSelfTestLimit
	if heavy {
		limit = heavySelfTestLimit
	}
	runCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	rs := c.canaries(runCtx, c, c.cfg.ScratchBase, verifier.CanaryOptions{SkipHeavy: !heavy})
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verifier.CanariesHold(rs); err != nil {
		c.proof.mu.Lock()
		c.proof.key = ""
		c.proof.mu.Unlock()
		return fmt.Errorf("sdconfine: %w", err)
	}
	if heavy {
		ranHeavy := false
		for _, r := range rs {
			ranHeavy = ranHeavy || (r.Heavy && r.Held())
		}
		if ranHeavy {
			c.proof.mu.Lock()
			c.proof.key, c.proof.at = key, c.sys.now()
			c.proof.mu.Unlock()
		}
	}
	return nil
}

func (c *Confiner) proofKey() string {
	list := Properties(Spec{Work: c.cfg.ScratchBase, Lim: probeLimits(), Caps: c.caps, PrivatePIDs: c.privatePIDs})
	sum := sha256.Sum256([]byte(strings.Join(list, "\n") + "|" + strconv.Itoa(c.version)))
	return hex.EncodeToString(sum[:])
}
