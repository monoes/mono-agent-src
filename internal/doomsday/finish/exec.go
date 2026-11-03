package finish

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The harness runs one machine criterion (note 13.6 sections 3, 5 and 6). It measures: the result is built from the exit
// status, the captured pipes and the post-run digests, and nothing the child writes is read as a result (R6).
//
// The environment is built from nothing: a fixed system PATH, HOME and TMPDIR in a per-criterion scratch directory, no
// inherited variable. The working directory is a fresh sealed copy of the snapshot. The process is born inside the run's
// containment unit (Place) and is killed with its process group on a limit; with no Place the criterion is not run at all.
//
// Enforced: the wall-clock deadline, the output cap, the containment placement, the post-run taint check, the process
// limits (limits.go: CPU, address space, open files, one file's size, process count), the scratch byte cap (a poll while
// the program runs and a check after it) and, where the kernel allows an unprivileged user namespace, the network denial
// (net_linux.go: a new empty network namespace). Where it does not, Net says "declared-only" and a finish that requires
// the denial parks (Harness.runCriteria).

// Defaults of a criterion's limits.
const (
	DefaultTimeout = 2 * time.Minute
	DefaultOutCap  = 1 << 20
	excerptBytes   = 1024
	// SystemPath is the only PATH a criterion sees; argv[0] is resolved on it by the harness.
	SystemPath = "/usr/local/bin:/usr/bin:/bin"
	// NetDeclared is what a result says about the network where nothing could deny it; NetEnforced where a new network
	// namespace did.
	NetDeclared = "denied(declared-only)"
	NetEnforced = "denied(enforced)"
	scratchPoll = 100 * time.Millisecond
)

// Results of a machine criterion, the closed list of note 13.6 section 8.
const (
	ResPass     = "pass"
	ResFail     = "fail"
	ResError    = "error"
	ResTimeout  = "timeout"
	ResOversize = "oversize"
	ResTainted  = "tainted"
	// resScratch is internal: the scratch cap was passed while the program ran; the recorded result is "error".
	resScratch = "scratch-cap"
)

// Measured is what one criterion measured, before the ledger binds it to a round.
type Measured struct {
	Result          string
	ExitCode        int
	StdoutDigest    string
	StderrDigest    string
	Stdout, Stderr  string // bounded excerpts, for the person
	Detail          string
	Net             string // NetEnforced or NetDeclared for a program that was run; empty for a file criterion
	RunsSubjectCode bool
	Tainted         []string
}

// Sandbox is the environment a criterion runs in.
type Sandbox struct {
	// Place makes cmd start inside the run's containment unit (stop.Placer.Place in production). Required: nil refuses
	// every execution, there is no fallback outside the unit.
	Place func(cmd *exec.Cmd) (release func(), err error)
	// Scratch is the directory the per-criterion working copies and scratch areas are made in.
	Scratch string
	Timeout time.Duration
	OutCap  int64
	// After is the injected clock for the deadline; nil is time.After.
	After func(time.Duration) <-chan time.Time
	// Path overrides SystemPath (tests).
	Path string
	// Limits are the process limits and the scratch cap; a zero field takes its default (limits.go).
	Limits Limits
	// RequireNetDeny makes the harness park (enforcement-required) where the network cannot be denied; without it the
	// denial is applied where possible and declared only where not.
	RequireNetDeny bool
	// NetDenyAvailable overrides the platform probe (tests); nil is the probe.
	NetDenyAvailable func() bool
}

// NetEnforceable says whether this sandbox can start a criterion without a network.
func (s Sandbox) NetEnforceable() bool {
	if s.NetDenyAvailable != nil {
		return s.NetDenyAvailable()
	}
	return netDenyAvailable()
}

func (s Sandbox) after(d time.Duration) <-chan time.Time {
	if s.After != nil {
		return s.After(d)
	}
	return time.After(d)
}

func fail(res, f string, a ...any) Measured {
	return Measured{Result: res, Detail: fmt.Sprintf(f, a...)}
}

// Run runs one criterion against the sealed snapshot at snap, whose tree is tree.
func (s Sandbox) Run(ctx context.Context, snap string, tree Tree, p Plan) Measured {
	if p.Kind == "file" {
		return evalFile(snap, p)
	}
	if s.Place == nil {
		return fail(ResError, "no containment unit is wired, so nothing is run outside one")
	}
	if got, err := DigestTree(snap, ""); err != nil || got.Digest != tree.Digest {
		return Measured{Result: ResTainted, Detail: "the sealed snapshot no longer has the round's digest", Tainted: Diff(tree, got, 20)}
	}
	work, err := s.workDir()
	if err != nil {
		return fail(ResError, "no scratch area: %v", err)
	}
	defer RemoveTree(work)
	subject := filepath.Join(work, "subject")
	if err := CopyTree(snap, subject, tree); err != nil {
		return fail(ResError, "the working copy could not be made: %v", err)
	}
	bin, err := s.resolve(p.Argv[0], work)
	if err != nil {
		return fail(ResError, "%v", err)
	}
	out := s.exec(ctx, work, subject, bin, p)
	out.RunsSubjectCode = true
	if after, derr := DigestTree(subject, ""); derr != nil || after.Digest != tree.Digest {
		out.Result, out.Tainted = ResTainted, Diff(tree, after, 20)
		out.Detail = "the criterion changed its working copy of the subject"
	} else if again, derr := DigestTree(snap, ""); derr != nil || again.Digest != tree.Digest {
		out.Result, out.Tainted = ResTainted, Diff(tree, again, 20)
		out.Detail = "the criterion changed the sealed snapshot"
	}
	return out
}

func (s Sandbox) workDir() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	w := filepath.Join(s.Scratch, "crit-"+hex.EncodeToString(b[:]))
	for _, d := range []string{w, filepath.Join(w, "scratch"), filepath.Join(w, "scratch", "home"), filepath.Join(w, "scratch", "tmp")} {
		if err := os.Mkdir(d, 0o700); err != nil {
			return "", err
		}
	}
	return w, nil
}

// resolve finds argv[0]: an absolute path outside the working area, or a name on the system PATH. A subject-relative
// executable is refused (it would be code the workers wrote; note 13.6 section 3).
func (s Sandbox) resolve(name, work string) (string, error) {
	if strings.Contains(name, "/") {
		if !filepath.IsAbs(name) || strings.HasPrefix(filepath.Clean(name)+"/", work+"/") {
			return "", fmt.Errorf("the program %q is not an absolute path outside the subject", name)
		}
		return filepath.Clean(name), nil
	}
	path := s.Path
	if path == "" {
		path = SystemPath
	}
	for _, d := range filepath.SplitList(path) {
		if !filepath.IsAbs(d) {
			continue
		}
		c := filepath.Join(d, name)
		if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return c, nil
		}
	}
	return "", fmt.Errorf("the program %q is not on the system path", name)
}

func (s Sandbox) env(work string) []string {
	path := s.Path
	if path == "" {
		path = SystemPath
	}
	sc := filepath.Join(work, "scratch")
	return []string{"PATH=" + path, "HOME=" + filepath.Join(sc, "home"), "TMPDIR=" + filepath.Join(sc, "tmp"),
		"XDG_CACHE_HOME=" + filepath.Join(sc, "tmp"), "XDG_CONFIG_HOME=" + filepath.Join(sc, "home"), "XDG_DATA_HOME=" + filepath.Join(sc, "home"), "LANG=C"}
}

func (s Sandbox) exec(ctx context.Context, work, dir, bin string, p Plan) Measured {
	limit := s.OutCap
	if limit <= 0 {
		limit = DefaultOutCap
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	over := make(chan struct{})
	var once sync.Once
	so, se := &capWriter{limit: limit, over: over, once: &once}, &capWriter{limit: limit, over: over, once: &once}
	lim := s.Limits.withDefaults(timeout)
	path, argv, ok := limitsArgv(lim, bin, p.Argv[1:])
	if !ok {
		return fail(ResError, "the process limits cannot be applied on this platform, so the program was not started")
	}
	cmd := exec.Command(path)
	cmd.Args = argv
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, s.env(work), so, se
	cmd.WaitDelay = 2 * time.Second
	setGroup(cmd)
	net := NetDeclared
	if s.NetEnforceable() {
		denyNet(cmd)
		net = NetEnforced
	}
	release, err := s.Place(cmd)
	if err != nil {
		return fail(ResError, "the criterion cannot be placed in the containment unit, so it was not started: %v", err)
	}
	defer release()
	if err := cmd.Start(); err != nil {
		return fail(ResError, "the program did not start: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	res := ""
	var werr error
	scratch := filepath.Join(work, "scratch")
	tick := time.NewTicker(scratchPoll)
	defer tick.Stop()
	timeUp := s.after(timeout)
wait:
	for {
		select {
		case werr = <-done:
			break wait
		case <-timeUp:
			res = ResTimeout
		case <-over:
			res = ResOversize
		case <-ctx.Done():
			res = ResError
		case <-tick.C:
			if _, big := scratchBytes(scratch, lim.ScratchBytes); !big {
				continue
			}
			res = resScratch
		}
		break
	}
	if res != "" {
		killGroup(cmd) // the program is not reaped yet, so its group id is still ours
		werr = <-done
	}
	// Nothing is signalled after a reap: a group id may be reused. What a program leaves behind is the unit's to end.
	o := Measured{StdoutDigest: so.sum(), StderrDigest: se.sum(), Stdout: so.excerpt(), Stderr: se.excerpt(), Net: net}
	if cmd.ProcessState != nil {
		o.ExitCode = cmd.ProcessState.ExitCode()
	}
	switch {
	case res == ResTimeout:
		o.Result, o.Detail = ResTimeout, "the deadline passed; the program was killed"
	case res == ResOversize || so.exceeded() || se.exceeded():
		o.Result, o.Detail = ResOversize, fmt.Sprintf("output beyond %d bytes; the program was killed", limit)
	case res == ResError:
		o.Result, o.Detail = ResError, "cancelled: "+ctx.Err().Error()
	case res == resScratch:
		o.Result, o.Detail = ResError, fmt.Sprintf("the scratch area grew beyond %d bytes; the program was killed", lim.ScratchBytes)
	case o.ExitCode == limitsFailExit && strings.HasPrefix(o.Stderr, limitsFailPrefix):
		o.Result, o.Detail = ResError, "the process limits could not be set, so the program did not run: "+strings.TrimSpace(strings.TrimPrefix(o.Stderr, limitsFailPrefix))
	default:
		o.Result, o.Detail = judge(p, o, so.buf.Bytes(), werr)
		if _, big := scratchBytes(scratch, lim.ScratchBytes); big {
			o.Result, o.Detail = ResError, fmt.Sprintf("the scratch area ended beyond %d bytes", lim.ScratchBytes)
		}
	}
	return o
}

// judge decides pass or fail from the exit status and the captured stdout, and nothing else.
func judge(p Plan, o Measured, stdout []byte, werr error) (string, string) {
	var ee *exec.ExitError
	if werr != nil && !errors.As(werr, &ee) {
		return ResError, "the program could not be waited for: " + werr.Error()
	}
	ok := false
	for _, c := range p.Exits {
		ok = ok || c == o.ExitCode
	}
	if !ok {
		return ResFail, "exit code " + strconv.Itoa(o.ExitCode) + " is not accepted"
	}
	if p.Compare && string(bytes.TrimSuffix(stdout, []byte("\n"))) != p.Stdout {
		return ResFail, "stdout is not the expected text"
	}
	return ResPass, ""
}

// evalFile checks a file criterion on the sealed snapshot without following links.
func evalFile(snap string, p Plan) Measured {
	cur := snap
	parts := strings.Split(p.Path, "/")
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		st, err := os.Lstat(cur)
		if err != nil {
			if p.Pred == "absent" && os.IsNotExist(err) {
				return Measured{Result: ResPass}
			}
			if os.IsNotExist(err) {
				return Measured{Result: ResFail, Detail: "the path does not exist"}
			}
			return fail(ResError, "%v", err)
		}
		if i < len(parts)-1 && !st.IsDir() {
			return Measured{Result: ResFail, Detail: "a path component is not a directory (links are not followed)"}
		}
		if i == len(parts)-1 {
			return judgeFile(cur, st, p)
		}
	}
	return fail(ResError, "empty path")
}

func judgeFile(path string, st os.FileInfo, p Plan) Measured {
	switch p.Pred {
	case "absent":
		return Measured{Result: ResFail, Detail: "the path exists"}
	case "exists":
		return Measured{Result: ResPass}
	}
	if !st.Mode().IsRegular() {
		return Measured{Result: ResFail, Detail: "the path is not a regular file"}
	}
	if p.Pred == "size" {
		n, _ := strconv.ParseInt(p.Arg, 10, 64)
		if st.Size() <= n {
			return Measured{Result: ResPass}
		}
		return Measured{Result: ResFail, Detail: "the file is larger than the bound"}
	}
	f, err := os.Open(path)
	if err != nil {
		return fail(ResError, "%v", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fail(ResError, "%v", err)
	}
	if hex.EncodeToString(h.Sum(nil)) == p.Arg {
		return Measured{Result: ResPass}
	}
	return Measured{Result: ResFail, Detail: "the file digests differently"}
}

// capWriter keeps the first limit bytes, hashes what it keeps, and closes over once more is written.
type capWriter struct {
	mu    sync.Mutex
	limit int64
	buf   bytes.Buffer
	more  bool
	over  chan struct{}
	once  *sync.Once
}

func (c *capWriter) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	room := c.limit - int64(c.buf.Len())
	if int64(len(b)) > room {
		c.buf.Write(b[:max(room, 0)])
		c.more = true
		c.once.Do(func() { close(c.over) })
		return len(b), nil
	}
	return c.buf.Write(b)
}

func (c *capWriter) sum() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := sha256.Sum256(c.buf.Bytes())
	return hex.EncodeToString(h[:])
}

func (c *capWriter) exceeded() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.more }

func (c *capWriter) excerpt() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.buf.Bytes()
	if len(b) > excerptBytes {
		b = b[:excerptBytes]
	}
	return strings.ToValidUTF8(string(b), "?")
}
