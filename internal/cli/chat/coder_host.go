package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/monomind"
)

// `coder host`: the process that keeps coder sessions going when the app and
// the terminal that started them are gone. One host serves a database; it takes
// the sessions' commands from the database (`coder sessions send|stop|…`), runs
// each turn as a detached `chat --conversation` process, and re-attaches to the
// turns still running when it starts. Stopping the host leaves the turns
// running; see docs/mastermind/plans/2026-10-26-coder-session-host.md.

const hostInfoFile = "host.json"

// hostInfo is what a running host writes next to its lock.
type hostInfo struct {
	PID       int    `json:"pid"`
	Stamp     string `json:"stamp"`
	Instance  string `json:"instance"`
	StartedAt string `json:"startedAt"`
}

func hostDir(cfg *Deps) string {
	return filepath.Join(filepath.Dir(clicore.ExpandPath(cfg.DBPath())), "coder-host")
}

func hostLockPath(cfg *Deps) string { return filepath.Join(hostDir(cfg), "host.lock") }

// hostRunning says whether a host holds the lock right now.
func hostRunning(cfg *Deps) bool {
	release, err := daemonhb.LockFile(hostLockPath(cfg))
	if err == nil {
		release()
		return false
	}
	return errors.Is(err, daemonhb.ErrHeld)
}

func readHostInfo(cfg *Deps) (hostInfo, bool) {
	raw, err := os.ReadFile(filepath.Join(hostDir(cfg), hostInfoFile))
	var h hostInfo
	if err != nil || json.Unmarshal(raw, &h) != nil {
		return hostInfo{}, false
	}
	return h, true
}

// hostExecutable is this binary's path. After an in-place upgrade Linux reports
// the replaced file as "<path> (deleted)"; the new one is at <path>.
func hostExecutable() (string, error) {
	exe, err := os.Executable()
	return strings.TrimSuffix(exe, " (deleted)"), err
}

// startHostProcess starts `coder host run` detached; a variable so tests do not
// start a real one.
var startHostProcess = func(cfg *Deps) error {
	exe, err := hostExecutable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(hostDir(cfg), 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(hostDir(cfg), "host.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(exe, "--db-path", cfg.DBPath(), "--profile", cfg.ProfileID(), "coder", "host", "run")
	cmd.Stdout, cmd.Stderr = logFile, logFile
	return clicore.DetachStart(cmd)
}

// ensureHost starts the host when none is running and waits for it to take its lock.
func ensureHost(cfg *Deps) error {
	if hostRunning(cfg) {
		return nil
	}
	if err := startHostProcess(cfg); err != nil {
		return fmt.Errorf("starting the coder host: %w", err)
	}
	for i := 0; i < 60; i++ {
		if hostRunning(cfg) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("the coder host did not start; see %s", filepath.Join(hostDir(cfg), "host.log"))
}

func newCoderHostCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "host",
		Short: "The process that keeps coder sessions running after the app closes",
		Long: "The coder host runs the turns of coder sessions as detached processes and takes commands for them " +
			"from the database (`coder sessions send`, `stop`, `pause`, `resume`). It starts by itself on the first " +
			"`coder sessions send`. Stopping it does not stop a running turn; the next host picks the turn up. " +
			"The host and the turns it starts keep the environment of the terminal that started the host.",
	}
	cmd.AddCommand(newCoderHostRunCmd(cfg), newCoderHostStatusCmd(cfg), newCoderHostStopCmd(cfg))
	return cmd
}

func newCoderHostRunCmd(cfg *Deps) *cobra.Command {
	var poll time.Duration
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the host in the foreground (sessions start it themselves)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := clicore.RequireOperator(cfg.RequireOperator, "run the coder host, which starts full-access turns"); err != nil {
				return err
			}
			if err := os.MkdirAll(hostDir(cfg), 0o700); err != nil {
				return err
			}
			// A `host status` or `sessions list` probes by taking the lock for a
			// moment, so a held lock is retried briefly before it counts as a host.
			var release func()
			var err error
			for i := 0; i < 10; i++ {
				if release, err = daemonhb.LockFile(hostLockPath(cfg)); !errors.Is(err, daemonhb.ErrHeld) {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if errors.Is(err, daemonhb.ErrHeld) {
				return clicore.InvalidInput("a coder host is already running for this database")
			} else if err != nil {
				return err
			}
			defer release()
			// host.json first: `host stop` and `host status` read it as soon as the lock is held.
			ownStamp := stampOf(os.Getpid())
			if ownStamp == "" {
				return fmt.Errorf("cannot read this process's start time, so `host stop` and a later host could not tell it from another process; not starting")
			}
			info := hostInfo{PID: os.Getpid(), Stamp: ownStamp, Instance: "host-" + uuid.NewString()[:8], StartedAt: time.Now().UTC().Format(time.RFC3339)}
			raw, _ := json.Marshal(info)
			infoPath := filepath.Join(hostDir(cfg), hostInfoFile)
			if err := os.WriteFile(infoPath+".tmp", raw, 0o600); err != nil { // renamed into place: a reader never sees half of it
				return err
			}
			if err := os.Rename(infoPath+".tmp", infoPath); err != nil {
				return err
			}
			defer os.Remove(infoPath) // while the lock is still held (defers run last-in first-out)
			sweepPrompts(filepath.Join(hostDir(cfg), "turns"), time.Hour)
			db, err := cfg.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			exe, err := hostExecutable()
			if err != nil {
				return err
			}
			logf := func(f string, a ...any) {
				fmt.Fprintf(cmd.ErrOrStderr(), time.Now().Format(time.RFC3339)+" "+f+"\n", a...)
			}
			h := &coderhost.Host{
				Store:                coderhost.NewStore(db.DB),
				L:                    &execLauncher{bin: exe, dbPath: cfg.DBPath(), instance: info.Instance, logDir: filepath.Join(hostDir(cfg), "turns"), logf: logf},
				Poll:                 poll,
				Logf:                 logf,
				RequireConversations: true,
			}
			if meshEnabled() {
				h.Mesh = &coderhost.HubMesh{StartHub: func(ctx context.Context) error { return startMeshHub(ctx, hostDir(cfg)) }, Logf: logf}
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			logf("coder host %s started (pid %d)", info.Instance, info.PID)
			err = h.Run(ctx)
			logf("coder host stopped; running turns keep going")
			return err
		},
	}
	cmd.Flags().DurationVar(&poll, "poll", 500*time.Millisecond, "How often the host looks for commands")
	return cmd
}

func hostStatus(cfg *Deps) map[string]any {
	st := map[string]any{"running": hostRunning(cfg)}
	if info, ok := readHostInfo(cfg); ok && st["running"] == true {
		st["pid"], st["instance"], st["startedAt"] = info.PID, info.Instance, info.StartedAt
	}
	return st
}

func newCoderHostStatusCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Whether the coder host is running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st := hostStatus(cfg)
			if cfg.JSONOutput() {
				return printQueueJSON(cmd, st)
			}
			if st["running"] == true {
				fmt.Fprintf(cmd.OutOrStdout(), "running  pid %v  since %v\n", st["pid"], st["startedAt"])
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "not running")
			}
			return nil
		},
	}
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

func newCoderHostStopCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the host; running turns keep going and the next host picks them up",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := clicore.RequireOperator(cfg.RequireOperator, "stop the coder host"); err != nil {
				return err
			}
			info, ok := readHostInfo(cfg)
			if !hostRunning(cfg) || !ok {
				return printQueueJSON(cmd, map[string]any{"stopped": false, "reason": "no host is running"})
			}
			if info.PID <= 1 || info.Stamp == "" || stampOf(info.PID) != info.Stamp {
				return clicore.InvalidInput("host.json names a process that is not the host any more; remove %s", filepath.Join(hostDir(cfg), hostInfoFile))
			}
			if err := terminateProcess(info.PID); err != nil {
				return err
			}
			for i := 0; i < 100 && hostRunning(cfg); i++ {
				time.Sleep(50 * time.Millisecond)
			}
			return printQueueJSON(cmd, map[string]any{"stopped": !hostRunning(cfg), "pid": info.PID})
		},
	}
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

// meshEnabled says whether the host shows its sessions on monomind's mesh.
// MONOMIND_MESH_AUTOJOIN=0 is monomind's own switch for presence; MONOAGENT_MESH=0 is ours.
func meshEnabled() bool {
	return os.Getenv("MONOMIND_MESH_AUTOJOIN") != "0" && os.Getenv("MONOAGENT_MESH") != "0"
}

// startMeshHub starts monomind's hub: any `monomind mesh` command starts it on first
// use, and it keeps running by itself. Without monomind there is no mesh.
var startMeshHub = func(ctx context.Context, dir string) error {
	bin, err := monomind.Find()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := monomind.CommandContext(ctx, bin, "mesh", "who") // node and environment pinned as for every monomind call
	cmd.Dir = dir                                           // the host's own folder, not a project: no project's version files choose what runs
	return cmd.Run()
}
