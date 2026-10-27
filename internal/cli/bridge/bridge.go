// Package bridge is the CLI's half of the Chrome extension bridge that a
// process owns (`extension serve`, the daemon, a workflow run that starts its
// own): the server as every owner wires it, finding a bridge that is already
// running, and waiting for a new one to bind.
package bridge

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/logging"
)

// Options are what NewServer wires into a bridge.
type Options struct {
	// ListenAddr is the address the server tries to bind.
	ListenAddr string
	// Profiles answers the popup's profile picker (profile.list).
	Profiles extension.ProfileSource
	// Tasks files the extension's task entries into the board (task.add).
	Tasks extension.TaskSink
	// Vault stores the files the side panel's confirm card saves (vault.save).
	Vault extension.VaultSink
	// Submits records form.submit_observed (a trusted submit seen in a parked
	// application's tab); nil leaves the method out.
	Submits extension.SubmitSink
	// FormFiller answers the side panel's form.fill (the profile's documents).
	FormFiller extension.FormFiller
	// PairMode reads the pairing mode (`extension autopair ask|auto|off`);
	// nil means ask.
	PairMode func() string
	// PairCards files a pending pairing claim as a Human in Loop card; nil
	// means no card (`extension approve` still decides).
	PairCards extension.PairCards
	// Version is this build, reported in the bridge's status.
	Version string
	// InstallCaptureSummaries wires the capture summaries and indexing into
	// the server, logging through logf.
	InstallCaptureSummaries func(srv *extension.Server, logf func(string, ...any))
}

// NewServer builds an extension server wired up the way every
// process that owns one needs it: answering the popup's profile picker,
// and reporting this build in its status. One place, so a bridge started
// by `extension serve` and one started by a workflow run are the same
// bridge with the same abilities.
func NewServer(logger zerolog.Logger, o Options) *extension.Server {
	srv := extension.NewServer(o.ListenAddr, logger)
	// The extension's profile picker asks this process which profiles
	// exist (profile.list). With no source installed the method is not
	// advertised at all, and the popup quietly saves into the default
	// profile — see internal/extension/profile_list.go.
	srv.SetProfileSource(o.Profiles)
	// task.add: the extension's task entries file into the board of the
	// profile they name (extension_tasks.go). Advertised in ping, so a newer
	// extension knows this bridge takes tasks.
	srv.SetTaskSink(o.Tasks)
	// vault.save: a file the person clicked Save on in the side panel's
	// confirm card goes into the media/file vault of the profile it names
	// (internal/extension/vault_save.go). Write only; advertised in ping.
	srv.SetVaultSink(o.Vault)
	// chat.send / chat.stop / chat.events: the side panel's chat, run as the
	// same `chat --conversation --turn` child the desktop app runs
	// (internal/extension/chat.go). Read-mostly tools only, never runs.
	srv.SetChatBackend(extension.NewCLIChatBackend())
	// form.fill: fill the page's form from the profile's own documents.
	srv.SetFormFiller(o.FormFiller)
	srv.SetSubmitSink(o.Submits)
	srv.SetPairMode(o.PairMode)
	srv.SetPairCards(o.PairCards)
	srv.SetVersion(o.Version)
	// Every bridge that owns the connection writes the summaries its
	// captures ask for. `extension serve` re-installs this with its own
	// narration (runExtensionServe).
	o.InstallCaptureSummaries(srv, LoggerLogf(logger))
	return srv
}

// FindRunning returns the status of an already-running bridge at one of
// addrs (HTTP base URLs) and the base URL it answered on, or ok=false when
// none is up. It stops probing once ctx is done, so a caller polling under a
// deadline cannot outlast it by more than one probe (each is bounded by the
// status probe timeout).
func FindRunning(ctx context.Context, addrs []string) (extension.Status, string, bool) {
	for _, addr := range addrs {
		if ctx.Err() != nil {
			break
		}
		if st, err := extension.FetchStatus(addr); err == nil {
			return st, addr, true
		}
	}
	return extension.Status{}, "", false
}

// BindTimeout bounds how long WaitForBind waits for the server
// to report a bound address. Binding a loopback port is immediate; this
// only exists so a pathological start reports something rather than
// hanging with no output at all.
const BindTimeout = 10 * time.Second

// WaitForBind blocks until the server reports the address it bound,
// or until Start fails, ctx is cancelled, or the timeout expires. Callers
// check ctx themselves: a cancellation here is a shutdown, not a failure.
func WaitForBind(ctx context.Context, srv *extension.Server, errCh <-chan error) (string, error) {
	deadline := time.Now().Add(BindTimeout)
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
			return "", fmt.Errorf("the bridge did not bind a port within %s", BindTimeout)
		}
	}
}

// NewServeLogger keeps the server's own logging out of the command's
// narration: `extension serve` prints the lifecycle itself, on stdout, in
// sentences. The server's warn-level events (a rejected handshake, a
// fallback port) still reach stderr, where they matter.
func NewServeLogger() zerolog.Logger {
	return logging.Zerolog().With().Str("component", "extension").Logger()
}

// LoggerLogf adapts a zerolog logger for the capture summaries.
func LoggerLogf(logger zerolog.Logger) func(string, ...any) {
	return func(format string, args ...any) { logger.Info().Msgf(format, args...) }
}

// Addr prefers the address the bridge says it bound, falling back to
// the URL it answered on — they differ only if a status document ever
// arrives without one, and a blank address is the least useful answer.
func Addr(st extension.Status, base string) string {
	if st.Addr != "" {
		return st.Addr
	}
	return strings.TrimPrefix(base, "http://")
}

// ListenAddr is the address this process's own extension server
// tries to bind: the env override (extension.ExtensionPortEnv) when set,
// otherwise the shared default port — whose EADDRINUSE fallback to 9323
// (e.g. when Chrome's own CDP holds 9222) lives in extension.Server.
func ListenAddr() string {
	if p := strings.TrimSpace(os.Getenv(extension.ExtensionPortEnv)); p != "" {
		return "127.0.0.1:" + p
	}
	return "127.0.0.1:" + extension.DefaultExtensionPort
}

// ProbeAddrs lists the HTTP base URLs where an already-running
// monoagent relay may be found: the env-overridden port when set, otherwise
// the default port and its 9323 fallback (mirroring extension.Server's bind
// candidates).
func ProbeAddrs() []string {
	if p := strings.TrimSpace(os.Getenv(extension.ExtensionPortEnv)); p != "" {
		return []string{"http://127.0.0.1:" + p}
	}
	return []string{
		"http://127.0.0.1:" + extension.DefaultExtensionPort,
		"http://127.0.0.1:" + extension.FallbackExtensionPort,
	}
}
