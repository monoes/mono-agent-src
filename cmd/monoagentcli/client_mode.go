package main

import (
	"bytes"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/agentclient"
	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/i18n"
	"github.com/monoes/mono-agent/internal/logging"
)

// Client mode. With MONOAGENT_API_URL and MONOAGENT_API_TOKEN both set, this binary is an agent's
// view of the daemon: the commands in clientModeClasses marked api-mapped go through the
// daemon's agent API (internal/agentclient) instead of the local database, and every other
// command either reads only embedded text (local-safe) or is refused with exit 3 and the code
// not_available_in_agent_mode. Nothing falls back to opening the database or the key store.
//
// Client mode only takes capability away. A refused command is never forwarded, so it cannot be
// used to pass an operator check, and the token is never printed.

// Classes of clientModeClasses.
const (
	cmAPIMapped    = "api-mapped"
	cmLocalSafe    = "local-safe"
	cmOperatorOnly = "operator-only"
	cmRefused      = "refused-in-agent-mode"
)

// codeNotAvailableInAgentMode is the code of a command client mode refuses.
const codeNotAvailableInAgentMode = "not_available_in_agent_mode"

const clientModeHint = "run it yourself, or ask the user; in agent mode `aitask kinds|status|result|complete|fail|cancel` work"

// notAvailableError is a refusal client mode gives: exit 3, code not_available_in_agent_mode.
type notAvailableError struct{ what, class string }

func (e *notAvailableError) Error() string {
	return "not available in agent mode: " + e.what + " (" + e.class + "); " + clientModeHint
}
func (e *notAvailableError) Unwrap() error { return &clicore.Error{Code: 3, Msg: e.Error()} }
func (e *notAvailableError) JSONErrorFields() map[string]any {
	return map[string]any{"code": codeNotAvailableInAgentMode, "hint": clientModeHint}
}

// errClientModeDB is what anything that would open the local database or its profile list says
// in client mode.
var errClientModeDB = &notAvailableError{what: "the local database", class: "client mode"}

// inClientMode says whether the environment asks for client mode.
func inClientMode() bool { return agentclient.FromEnv() != nil }

// clientModeClass is the class of the command with this key: its own entry, else the nearest
// ancestor's when that is a refusal. A child of an api-mapped or local-safe command is not
// covered by its parent: it needs an entry of its own, so a new subcommand cannot start working
// in this mode by being added. ok is false for a command with no class.
func clientModeClass(key string) (class string, ok bool) {
	if c, found := clientModeClasses[key]; found {
		return c, true
	}
	for k := key; ; {
		i := strings.LastIndexByte(k, ' ')
		if i < 0 {
			return "", false
		}
		k = k[:i]
		if c, found := clientModeClasses[k]; found {
			if c == cmOperatorOnly || c == cmRefused {
				return c, true
			}
			return "", false
		}
	}
}

// scrubbedOutput holds back everything a client-mode process prints and writes it at the end with
// both credentials (the API bearer and the task token) replaced by value: a locally generated
// error that repeats an argument (`--workers "$MONOAGENT_API_TOKEN"`) or a log line must not print one.
type scrubbedOutput struct {
	dst     io.Writer
	buf     bytes.Buffer
	secrets []string
}

func (o *scrubbedOutput) Write(b []byte) (int, error) { return o.buf.Write(b) }

func (o *scrubbedOutput) scrub(s string) string {
	for _, v := range o.secrets {
		if len(v) >= 6 {
			s = strings.ReplaceAll(s, v, "[token]")
		}
	}
	return s
}

func (o *scrubbedOutput) flush() {
	_, _ = io.WriteString(o.dst, o.scrub(o.buf.String()))
	o.buf.Reset()
}

// runClientModeProcess is run in client mode: the logger, the locale and the command, with every
// byte of output scrubbed and no log file (a file would keep what the scrubbing hides from the terminal).
func runClientModeProcess(args []string, stdout, stderr io.Writer) int {
	secrets := []string{os.Getenv(agentclient.EnvToken), os.Getenv(aitask.EnvToken)}
	out := &scrubbedOutput{dst: stdout, secrets: secrets}
	errw := &scrubbedOutput{dst: stderr, secrets: secrets}
	defer out.flush()
	defer errw.flush()

	logArgs := args
	if _, wantsFile := logging.FileFlag(args); wantsFile {
		logArgs = nil // the level flag is not read either: nothing is opened
	}
	logging.SetLineFilter(func(s string) string { return errw.scrub(s) })
	logging.Init(logArgs, os.Getenv, errw)
	i18n.SetLocale(i18n.Detect(args))
	if _, wantsFile := logging.FileFlag(args); wantsFile {
		refusal := &notAvailableError{what: "--log-file", class: "client mode"}
		reportCommandError(args, refusal, out, errw)
		return exitCodeFor(refusal)
	}
	return runClientMode(args, out, errw, func() int { return runLocal(args, out, errw) })
}

// runClientMode runs one invocation in client mode and returns the exit code. api-mapped
// commands run here, with no account gate, process guard or database (the daemon enforces its
// own); local-safe ones run as they always do, gate included, so client mode opens nothing the
// gate keeps shut.
func runClientMode(args []string, stdout, stderr io.Writer, local func() int) int {
	root := newProbeRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	root.AddCommand(&cobra.Command{Use: cobra.ShellCompRequestCmd, Aliases: []string{cobra.ShellCompNoDescRequestCmd},
		Hidden: true, DisableFlagParsing: true}) // cobra adds __complete only while it runs it
	target, _, err := root.Find(args)
	if err != nil || target == nil || target == root || accountGate().AsksForHelp(args) {
		// A bare root, an unknown command and --help print help or an error and run nothing.
		return local()
	}
	key := commandKey(target)
	class, ok := clientModeClass(key)
	switch {
	case ok && class == cmLocalSafe:
		return local()
	case ok && class == cmAPIMapped:
		cmd := newRunRoot()
		cmd.SetOut(stdout)
		cmd.SetErr(stderr)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			reportCommandError(args, err, stdout, stderr)
			return exitCodeFor(err)
		}
		return 0
	}
	if !ok {
		class = cmRefused // a command with no class is refused: default deny
	}
	refusal := &notAvailableError{what: "monoagentcli " + key, class: class}
	reportCommandError(args, refusal, stdout, stderr)
	return exitCodeFor(refusal)
}
