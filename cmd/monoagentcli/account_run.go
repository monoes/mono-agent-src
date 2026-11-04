package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"os/signal"
	"time"

	"github.com/monoes/mono-agent/internal/account"
)

// reportGateRefusal prints a refusal: its text on stderr and, when --json
// appears anywhere in the arguments, the one JSON document the commands' own
// wrappers would have printed (the gate runs before them, so it prints it).
func reportGateRefusal(args []string, err error, stdout, stderr io.Writer) {
	fmt.Fprintln(stderr, err)
	if !argsWantJSON(args) {
		return
	}
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

// argsWantJSON reports whether --json is among the arguments. The value of a
// global value flag is skipped, so `--profile --json`
// names a profile.
func argsWantJSON(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--json" || a == "--json=true" {
			return true
		}
		if name, isFlag := valueFlagName(a); isFlag && name != "" {
			i++
		}
	}
	return false
}

// newDefaultGuard builds the guard of a real process; a test swaps it to see
// what happens when none can be built.
var newDefaultGuard = account.NewDefaultGuard

// newRunRoot builds the command tree that run executes; a test wraps every
// command's hooks to see that the process guard is installed whenever one runs.
var newRunRoot = newRootCmd

// processGuard returns this process's account guard and what to call when the
// command is done. A guard installed before run (only a test does that) is
// used as it is. Otherwise run builds and installs the default guard over
// ~/.monoagent/account, which creates nothing until a guard pass or a sign-in
// writes something: from the enforcement date the first pass of a machine that
// never signed in writes the clock-guard record (A25). If it cannot be built
// (no home directory) there is none: a gated command then fails closed once
// enforcement is on, and runs before, judged on the clock alone, since without
// a guard no record can be read (and such a process has no home to hold one).
func processGuard() (*account.Guard, func()) {
	if g := account.Current(); g != nil {
		return g, func() {}
	}
	g, err := newDefaultGuard()
	if err != nil {
		return nil, func() {}
	}
	account.Install(g)
	return g, func() {
		account.Install(nil)
		g.Close()
	}
}

// cancelsOnRefusal says whether a command of this class is ended when the
// login is refused while it runs (spec §6.4): only a gated one-shot command.
// A serving command keeps serving and refuses each call (D8); an open one does
// not depend on the login.
func cancelsOnRefusal(class string) bool { return class == classGated }

// cancelWhenRefused ends a command's work when monoes.me refuses the login
// while it runs. A test swaps it to see who is given it.
var cancelWhenRefused = func(g *account.Guard, cancel context.CancelFunc) {
	g.OnRefused(func(st account.Status) {
		if !st.Allowed() {
			cancel()
		}
	})
}

// afterFunc and startRefresher are what armLateRefresher uses; a test swaps
// them to see the timer armed and fired without waiting five minutes.
var (
	afterFunc      = time.AfterFunc
	startRefresher = func(ctx context.Context, g *account.Guard) { g.StartRefresher(ctx) }
)

// notifyContext is signal.NotifyContext; a test swaps it to see when run lets go of the signals.
var notifyContext = signal.NotifyContext

// armLateRefresher starts g's background refresher once the process has run
// for account.LateRefresher, so a long `workflow run` or `chat` keeps its
// login fresh (spec §6.4). The serving commands start it at once in their own
// RunE. The returned func disarms it: a command that ends sooner never starts it.
func armLateRefresher(ctx context.Context, g *account.Guard) (disarm func()) {
	t := afterFunc(account.LateRefresher, func() { startRefresher(ctx, g) })
	return func() { t.Stop() }
}

// isSignedOutOnlyStop reports whether args resolve to `doomsday stop`, the one verb of the open-ended mode that works
// signed out (account gate class open). Its process arms no late refresher: a Stop that runs past five minutes must
// not contact monoes.me or read the key store (the user's rule: Stop never depends on the sign-in).
func isSignedOutOnlyStop(root *cobra.Command, args []string) bool {
	c, _, err := root.Find(args)
	return err == nil && c != nil && commandKey(c) == "doomsday stop"
}
