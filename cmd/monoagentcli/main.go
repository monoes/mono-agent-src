package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/i18n"
	"github.com/monoes/mono-agent/internal/logging"
)

// version/buildDate are set via -ldflags in release builds (see
// .github/workflows/release.yml); empty in a plain `go build`.
var (
	version   = ""
	buildDate = ""
)

// getVersion/getBuildDate resolve version/buildDate lazily via sync.Once
// rather than an unconditional `init()`, which used to shell out to `git
// describe` on EVERY invocation regardless of subcommand — including a bare
// `--help` — costing up to several seconds on a cold cache. monoagentcli is
// spawned as a subprocess repeatedly by the Wails app (each chat message,
// each agent scan, each org observe call), so that fixed per-invocation tax
// was a real, compounding source of UI latency in dev builds. Release
// builds never hit this path at all since version is baked in via ldflags.
var (
	versionOnce sync.Once
	buildOnce   sync.Once
)

func getVersion() string {
	versionOnce.Do(func() {
		if version != "" {
			return
		}
		if out, err := exec.Command("git", "describe", "--tags", "--always").Output(); err == nil {
			version = strings.TrimSpace(string(out))
		} else {
			version = "dev"
		}
	})
	return version
}

func getBuildDate() string {
	buildOnce.Do(func() {
		if buildDate == "" {
			buildDate = time.Now().UTC().Format("2006-01-02T15:04:05Z")
		}
	})
	return buildDate
}

func main() {
	// Report-then-repanic: files a crash report as a side effect, then
	// re-panics so the process still crashes with the original trace and
	// exit behavior a user would otherwise see — this only observes, it
	// never swallows the panic.
	defer func() {
		if r := recover(); r != nil {
			reportCrash(r, debug.Stack())
			panic(r)
		}
	}()

	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the command line without the process: it gates args on the monoes.me
// account (account_gate.go), executes them and returns the exit code, so a
// test can call it. Tests that call newRootCmd().Execute() skip the gate by
// design; the engine and the runners judge the account themselves.
func run(args []string, stdout, stderr io.Writer) int {
	if args == nil {
		args = []string{} // cobra reads os.Args for a nil slice
	}
	// The root logger, before anything can log (see internal/logging).
	logging.Init(args, os.Getenv, os.Stderr)

	// Locale must be resolved before newRootCmd() builds the command tree,
	// since cobra Short/Long/Example strings are evaluated once at
	// construction time, before flags are parsed. See internal/i18n and
	// docs/i18n.md.
	i18n.SetLocale(i18n.Detect(args))

	// The guard is installed before anything of a command can run, and released after it has returned:
	// Require and CurrentStatus with no guard judge the enforcement date on the clock alone (the
	// clock-guard record is read only through a guard), and every gate site of this binary runs inside
	// a command. It is built before the signals are caught, so that the signals are let go before the
	// guard is released: deferred calls run last in, first out. release (the guard's Close) waits for a
	// refresh grant that monoes.me is still answering, up to the guard's call timeout, because a grant
	// that was sent is never abandoned (A20), and signal.NotifyContext keeps catching the signals
	// until its stop function runs. With the guard released first, a second Ctrl-C during that wait
	// would be swallowed and only SIGKILL would end the process.
	g, release := processGuard()
	defer release()

	// SIGHUP too: a CLI whose terminal or parent goes away must cancel, so
	// commands end what they started (monoes/mono-agent#235).
	ctx, cancel := notifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()

	root := newRunRoot()
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(args)
	applyClassification(root)

	if g != nil {
		defer armLateRefresher(ctx, g)()
	}

	// The older login is adopted before the gate judges (spec D23), or a gated command would be refused first.
	adoptBeforeGate(ctx, args)

	// The gate comes before anything runs: no first-run check, no database.
	if err := gateCommand(ctx, root, args, g, stderr); err != nil {
		reportGateRefusal(args, err, stdout, stderr)
		return exitCodeFor(err)
	}
	if g != nil && cancelsOnRefusal(invocationClass(root, args)) {
		cancelWhenRefused(g, cancel)
	}
	if err := root.ExecuteContext(ctx); err != nil {
		reportCommandError(args, err, stdout, stderr)
		return exitCodeFor(err)
	}
	return 0
}

// reportCommandError prints a failed command's error on stderr and, when
// --json was passed, as {"error": …, "code": …} on stdout with the same
// exit code: "code" is the failure's class (jsonErrorCode) and an error that
// knows its machine-readable form adds its own fields (login_required, a
// hint, …). A reportedError already printed its JSON result (e.g. `org
// validate` on an invalid org, or withJSONErrors' own {"error"}), so nothing
// is added: stdout stays exactly one document.
func reportCommandError(args []string, err error, stdout, stderr io.Writer) {
	// A failure the command's own output showed (a human-mode run's
	// summary) is not repeated — unless a wrapper added to it (a hint).
	var shown clicore.Shown
	if !argsWantJSON(args) && errors.As(err, &shown) && shown.Error() == err.Error() {
		return
	}
	fmt.Fprintln(stderr, err)
	var reported reportedError
	if argsWantJSON(args) && !errors.As(err, &reported) {
		body := map[string]any{"error": err.Error()}
		if code := jsonErrorCode(err); code != "" {
			body["code"] = code
		}
		var fields jsonErrorFields
		if errors.As(err, &fields) {
			for k, v := range fields.JSONErrorFields() {
				if k != "error" {
					body[k] = v
				}
			}
		}
		b, _ := json.Marshal(body)
		fmt.Fprintln(stdout, string(b))
	}
}

// globalValueFlags are the root persistent flags that take a separate value
// argument (root.go). Boolean flags are absent on purpose: they never consume
// the next arg.
var globalValueFlags = map[string]bool{
	"--log-file": true, "--db-path": true, "--output-dir": true,
	"--config-dir": true, "--workers": true, "--profile": true, "--lang": true,
	"--log-level": true,
}

// valueFlagName reports whether arg is one of those flags, and returns its
// name when the value is still to come in the next arg ("" for `--flag=value`,
// which carries its own).
func valueFlagName(arg string) (string, bool) {
	if name, _, ok := strings.Cut(arg, "="); ok {
		return "", globalValueFlags[name]
	}
	return arg, globalValueFlags[arg]
}
