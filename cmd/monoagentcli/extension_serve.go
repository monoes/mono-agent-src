package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/extension"
)

// The long-lived bridge.
//
// Until this command existed the extension server's whole lifetime was one
// workflow node's run (see setupExtensionBridge in node.go), which was
// defensible while the extension only ever served workflows. It is not
// defensible now: the capture shortcut, the popup's save button, the recall
// panel and the offline queue's flush are all things the *user* starts, at
// a moment no workflow is running — so the extension spent almost all of
// its time reporting "Disconnected", correctly.
//
// `extension serve` is the missing half: one process that holds the port
// for as long as the user wants the browser wired up. Everything else is
// unchanged, and deliberately so — setupExtensionBridge already probes for
// a running server and relays through it, so a workflow that starts while
// this is up reuses it by the path that was already there.

// bridgeAdvice is appended to every "the extension never turned up" error.
// The first thing a person hit was a red "Disconnected" with no account of
// why or what to do, and the answer is almost always this one: nothing is
// holding the bridge open.
const bridgeAdvice = "\n\nThe bridge only exists while something is running it. To keep the browser wired up " +
	"(capture shortcut, side panel, recall), run `monoagentcli extension serve` in a terminal and leave it " +
	"running. `monoagentcli extension status` says whether one is up and whether the extension is attached, " +
	"and `monoagentcli extension pair` prints the token the extension side panel needs."

// bridgeLifetimeHint returns the advice above, but only for a bridge this
// process owns. When the caller is relaying through someone else's bridge
// (a *extension.RemoteBridge, which binds nothing and so implements
// neither addrProvider nor pairingURLProvider) a long-lived bridge is
// already running, and telling the user to start one would send them to
// fix the one thing that is demonstrably fine.
func bridgeLifetimeHint(bridge connChecker) string {
	if _, ownsPort := bridge.(addrProvider); !ownsPort {
		return ""
	}
	return bridgeAdvice
}

// newExtensionServer builds an extension server wired up the way every
// process that owns one needs it: answering the popup's profile picker,
// and reporting this build in its status. One place, so a bridge started
// by `extension serve` and one started by a workflow run are the same
// bridge with the same abilities.
func newExtensionServer(logger zerolog.Logger) *extension.Server {
	srv := extension.NewServer(extensionListenAddr(), logger)
	// The extension's profile picker asks this process which profiles
	// exist (profile.list). With no source installed the method is not
	// advertised at all, and the popup quietly saves into the default
	// profile — see internal/extension/profile_list.go.
	srv.SetProfileSource(extensionProfileSource(defaultDBPath))
	// task.add: the extension's task entries file into the board of the
	// profile they name (extension_tasks.go). Advertised in ping, so a newer
	// extension knows this bridge takes tasks.
	srv.SetTaskSink(extensionTaskSink(defaultDBPath))
	// chat.send / chat.stop / chat.events: the side panel's chat, run as the
	// same `chat --conversation --turn` child the desktop app runs
	// (internal/extension/chat.go). Read-mostly tools only, never runs.
	srv.SetChatBackend(extension.NewCLIChatBackend())
	// form.fill: fill the page's form from the profile's own documents
	// (extension_formfill.go).
	srv.SetFormFiller(extensionFormFiller(defaultDBPath, configuredSummaryRuntime()))
	srv.SetVersion(getVersion())
	// Every bridge that owns the connection writes the summaries its
	// captures ask for. `extension serve` re-installs this with its own
	// narration (runExtensionServe).
	installCaptureSummaries(srv, loggerLogf(logger))
	return srv
}

// findRunningBridge returns the status of an already-running bridge and
// the base URL it answered on, or ok=false when none is up. It checks the
// same addresses setupExtensionBridge probes, so both agree on what
// "already running" means.
func findRunningBridge() (extension.Status, string, bool) {
	return findRunningBridgeCtx(context.Background())
}

// findRunningBridgeCtx is findRunningBridge that stops probing once ctx is
// done, so a caller polling under a deadline cannot outlast it by more than
// one probe (each is bounded by the status probe timeout).
func findRunningBridgeCtx(ctx context.Context) (extension.Status, string, bool) {
	for _, addr := range extensionProbeAddrs() {
		if ctx.Err() != nil {
			break
		}
		if st, err := extension.FetchStatus(addr); err == nil {
			return st, addr, true
		}
	}
	return extension.Status{}, "", false
}

func newExtensionServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the extension bridge in the foreground so the browser stays connected",
		Long: "Holds the extension bridge port open until interrupted (Ctrl+C), so the\n" +
			"MonoAgent Bridge extension can stay connected whether or not a workflow is\n" +
			"running. Without this, the bridge only exists for the lifetime of the command\n" +
			"that started it, and the extension reports \"Disconnected\" the rest of the\n" +
			"time — which is why the capture shortcut, the side panel's save button and the\n" +
			"recall panel appeared to do nothing.\n" +
			"\n" +
			"Other monoagentcli commands (`workflow run`, `capture page`, `login`) detect a\n" +
			"bridge that is already running and relay through it rather than starting a\n" +
			"second one, so leaving this running costs them nothing.\n" +
			"\n" +
			"Binds 127.0.0.1:" + extension.DefaultExtensionPort + ", falling back to " +
			extension.FallbackExtensionPort + " when another program (usually a Chrome\n" +
			"started with --remote-debugging-port) already holds it; override with " +
			extension.ExtensionPortEnv + ".\n" +
			"\n" +
			"The extension's service worker is suspended by Chrome whenever it goes idle\n" +
			"and respawns on the next event, so the socket dropping and coming back is\n" +
			"normal and is reported as such, not as an error.\n" +
			"\n" +
			"Captures saved with \"Save page summary\" or \"Save video summary\" get an AI\n" +
			"summary (summary.md) written beside them in the background, one at a time, by\n" +
			"the agent runtime named by --summary-runtime, else $" + summaryRuntimeEnv + ", else\n" +
			"\"claude\" (see `agent scan --installed`; \"off\" disables them).",
		Example: "  monoagentcli extension serve\n  MONOAGENT_EXTENSION_PORT=9400 monoagentcli extension serve\n" +
			"  monoagentcli extension serve --summary-runtime codex",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runExtensionServe(ctx, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&summaryRuntimeFlag, "summary-runtime", "",
		"Agent runtime that writes capture summaries (default: $"+summaryRuntimeEnv+", else claude; \"off\" disables)")
	return servingCommand(cmd)
}

// runExtensionServe is the body of `extension serve`, split out so its
// lifecycle can be tested without a signal handler or a process to send a
// signal to.
func runExtensionServe(ctx context.Context, out io.Writer) error {
	// Probe before binding, exactly as setupExtensionBridge does. Without
	// this a second `extension serve` would lose the race for the default
	// port, quietly take the 9323 fallback, and leave two bridges
	// competing for one extension — the worst outcome of the three.
	if st, base, ok := findRunningBridge(); ok {
		fmt.Fprintf(out, "A bridge is already running on %s (pid %d) — nothing to do.\n",
			bridgeAddr(st, base), st.PID)
		fmt.Fprintf(out, "  Extension: %s\n", bridgeExtensionLine(st))
		return nil
	}

	srv := newExtensionServer(newBridgeServeLogger())
	summaries := installCaptureSummaries(srv, narrateLogf(out))
	defer summaries.Close()
	summaries.sweepIndexAfter(ctx, captureIndexStartupDelay)
	errCh := srv.StartAsync(ctx)
	defer srv.Close() //nolint:errcheck

	addr, err := waitForBridgeBind(ctx, srv, errCh)
	if err != nil {
		if ctx.Err() != nil {
			// Interrupted while still coming up. Nothing went wrong and
			// nothing was left behind; saying "cannot start the bridge"
			// because the user pressed Ctrl+C would be a lie.
			return nil
		}
		// Both candidate ports failed to bind and neither is a bridge we
		// could have relayed through (findRunningBridge already said so),
		// so this is a real conflict the user has to resolve.
		return errAuthConnection("cannot start the extension bridge: %v\n"+
			"Another program is holding %s (most often a Chrome started with --remote-debugging-port). "+
			"Close it, or set %s to a free port.",
			err, strings.Join(extensionProbeAddrs(), " and "), extension.ExtensionPortEnv)
	}

	fmt.Fprintf(out, "MonoAgent bridge listening on %s\n", addr)
	fmt.Fprintf(out, "  Extension socket: ws://%s/monoagent\n", addr)
	fmt.Fprintf(out, "  Pairing token:    monoagentcli extension pair (paste it into the extension side panel)\n")
	fmt.Fprintf(out, "  Status:           monoagentcli extension status\n")
	fmt.Fprintf(out, "  Summaries:        %s (--summary-runtime / %s)\n", configuredSummaryRuntime(), summaryRuntimeEnv)
	fmt.Fprintln(out, "Waiting for the extension. Press Ctrl+C to stop.")

	watchBridgeConnection(ctx, srv, out)
	<-ctx.Done()
	fmt.Fprintln(out, "Shutting down the bridge.")
	return nil
}

// bridgeBindTimeout bounds how long runExtensionServe waits for the server
// to report a bound address. Binding a loopback port is immediate; this
// only exists so a pathological start reports something rather than
// hanging with no output at all.
const bridgeBindTimeout = 10 * time.Second

// waitForBridgeBind blocks until the server reports the address it bound,
// or until Start fails, ctx is cancelled, or the timeout expires. Callers
// check ctx themselves: a cancellation here is a shutdown, not a failure.
func waitForBridgeBind(ctx context.Context, srv *extension.Server, errCh <-chan error) (string, error) {
	deadline := time.Now().Add(bridgeBindTimeout)
	for {
		if addr, ok := srv.Addr(); ok {
			return addr, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case err, open := <-errCh:
			if err != nil {
				return "", err
			}
			if !open {
				return "", fmt.Errorf("the bridge stopped before it bound a port")
			}
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("the bridge did not bind a port within %s", bridgeBindTimeout)
		}
	}
}

// bridgeWatchInterval is how often the serve loop re-reads the connection
// state to narrate it. A second is well under a person's patience and far
// above the cost of a mutex read.
const bridgeWatchInterval = time.Second

// watchBridgeConnection narrates the extension attaching and detaching, so
// the terminal answers "is my browser wired up right now?" without anyone
// having to run another command. Chrome suspends an idle MV3 service
// worker and respawns it on the next event, so a socket that comes and
// goes is the normal state of affairs and is described as such — said once,
// because saying it every time would train people to ignore the line.
func watchBridgeConnection(ctx context.Context, srv *extension.Server, out io.Writer) {
	go func() {
		ticker := time.NewTicker(bridgeWatchInterval)
		defer ticker.Stop()
		prev := srv.Browsers()
		explained := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cur := srv.Browsers()
				stamp := time.Now().Format("15:04:05")
				for _, line := range narrateBrowsers(prev, cur, &explained) {
					fmt.Fprintf(out, "[%s] %s\n", stamp, line)
				}
				prev = cur
			}
		}
	}()
}

// narrateBrowsers says which browsers attached, reattached or left between
// two looks at the bridge. Each browser profile has its own socket, so a
// line names the browser (its label, else its short id) and the profile it
// runs; a reconnect is a new connectedAt for the same instance.
func narrateBrowsers(prev, cur []extension.ConnInfo, explained *bool) []string {
	before := make(map[string]extension.ConnInfo, len(prev))
	for _, b := range prev {
		before[b.Instance] = b
	}
	var lines []string
	now := make(map[string]bool, len(cur))
	for _, b := range cur {
		now[b.Instance] = true
		old, had := before[b.Instance]
		switch {
		case !had:
			lines = append(lines, "extension connected: "+browserName(b))
		case !old.ConnectedAt.Equal(b.ConnectedAt):
			lines = append(lines, "extension reconnected: "+browserName(b))
		}
	}
	for _, b := range prev {
		if now[b.Instance] {
			continue
		}
		line := "extension disconnected: " + browserName(b)
		if !*explained {
			*explained = true
			line += " — normal: Chrome suspends the extension when it goes idle and it reconnects on the next event."
		}
		lines = append(lines, line)
	}
	return lines
}

// browserName is how the bridge's narration refers to one browser.
func browserName(b extension.ConnInfo) string {
	name := b.Label
	if name == "" {
		name = b.Instance
		if len(name) > 8 {
			name = name[:8]
		}
	}
	if b.Legacy {
		return name + " (extension older than 1.5.0)"
	}
	if b.Profile == "" {
		return name + " (any profile)"
	}
	return name + " (profile " + b.Profile + ")"
}

// newBridgeServeLogger keeps the server's own logging out of the command's
// narration: `extension serve` prints the lifecycle itself, on stdout, in
// sentences. The server's warn-level events (a rejected handshake, a
// fallback port) still reach stderr, where they matter.
func newBridgeServeLogger() zerolog.Logger {
	return zerolog.New(os.Stderr).With().Timestamp().Str("component", "extension").Logger().Level(zerolog.WarnLevel)
}

// bridgeStatusReport is `extension status --json`. Running is separate from
// the status document because "nothing is listening" is not a state the
// bridge itself can report — there is no bridge to report it.
type bridgeStatusReport struct {
	Running bool              `json:"running"`
	Bridge  *extension.Status `json:"bridge,omitempty"`
	// ClientPairing says whether this client's pairing token is the
	// bridge's ("ok", "mismatch", or "unknown" for a bridge too old to
	// ask). A mismatch means every relayed command from here gets 401.
	ClientPairing extension.PairingState `json:"clientPairing,omitempty"`
	Hint          string                 `json:"hint,omitempty"`
}

func newExtensionStatusCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report whether a bridge is running and whether the extension is attached",
		Long: "Says which of the three situations you are in, which the extension side panel's\n" +
			"\"Disconnected\" could not previously tell apart:\n" +
			"\n" +
			"  - no bridge is running at all (start one with `extension serve`);\n" +
			"  - a bridge is running and the extension is attached;\n" +
			"  - a bridge is running but turned the extension away for a bad or missing\n" +
			"    pairing token (run `extension pair`).\n" +
			"\n" +
			"An extension that is simply idle shows as \"not connected\": Chrome suspends the\n" +
			"service worker when nothing is happening and it reconnects on the next event.",
		Example: "  monoagentcli extension status\n  monoagentcli extension status --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExtensionStatus(cmd.OutOrStdout(), cfg.JSONOutput)
		},
	}
}

// runExtensionStatus prints what `extension status` found. It exits 0
// either way: "no bridge is running" is an answer to the question asked,
// not a failure to answer it.
func runExtensionStatus(out io.Writer, asJSON bool) error {
	st, base, ok := findRunningBridge()
	if !ok {
		report := bridgeStatusReport{
			Hint: "No bridge is running. Start one with `monoagentcli extension serve`.",
		}
		if asJSON {
			return writeBridgeJSON(out, report)
		}
		fmt.Fprintf(out, "No bridge is running on %s.\n", strings.Join(extensionProbeAddrs(), " or "))
		fmt.Fprintln(out, "  Start one with: monoagentcli extension serve")
		return nil
	}

	// /monoagent/health is unauthenticated, so "connected" says nothing
	// about whether THIS client may use the bridge; ask with our token.
	pairing, _ := extension.CheckPairing(base)
	if asJSON {
		report := bridgeStatusReport{Running: true, Bridge: &st, ClientPairing: pairing}
		if pairing == extension.PairingMismatch {
			report.Hint = extension.PairingMismatchHint
		}
		return writeBridgeJSON(out, report)
	}
	fmt.Fprintf(out, "Bridge running on %s (pid %d, up %s)\n",
		bridgeAddr(st, base), st.PID, bridgeUptime(st))
	if pairing == extension.PairingMismatch {
		fmt.Fprintf(out, "  This client: %s\n", extension.PairingMismatchHint)
	}
	fmt.Fprintf(out, "  Extension: %s\n", bridgeExtensionLine(st))
	if pairing != extension.PairingMismatch && st.Browsers > 1 {
		fmt.Fprintf(out, "  Browsers: %d attached — see `monoagentcli extension browsers`\n", st.Browsers)
	}
	if st.Status == extension.StatusUnpaired {
		fmt.Fprintln(out, "  Pair it with: monoagentcli extension pair (paste the token into the extension side panel)")
	}
	return nil
}

func writeBridgeJSON(out io.Writer, report bridgeStatusReport) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

// bridgeAddr prefers the address the bridge says it bound, falling back to
// the URL it answered on — they differ only if a status document ever
// arrives without one, and a blank address is the least useful answer.
func bridgeAddr(st extension.Status, base string) string {
	if st.Addr != "" {
		return st.Addr
	}
	return strings.TrimPrefix(base, "http://")
}

// bridgeExtensionLine renders the three states as the sentence each one
// needs, rather than a state name the reader has to interpret.
func bridgeExtensionLine(st extension.Status) string {
	switch st.Status {
	case extension.StatusConnected:
		return "connected"
	case extension.StatusUnpaired:
		return "not connected — the bridge turned it away for a bad or missing pairing token"
	default:
		return "not connected (idle is normal: Chrome suspends the extension and it reconnects on the next event)"
	}
}

func bridgeUptime(st extension.Status) string {
	return (time.Duration(st.UptimeSec) * time.Second).String()
}
