package nodejs

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/doctor"
	"github.com/monoes/mono-agent/internal/nodemgr"
)

// NewCommand is `nodejs`. `node` is taken by the workflow node runner, hence `nodejs`.
func NewCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "nodejs",
		Short: "Manage the private Node.js runtime monoagent can install for monomind",
		Long: `monomind and the npm-based AI agent CLIs need Node.js >= ` + nodemgr.MinVersion + `.
When the machine has no suitable Node, monoagent can download one into
~/.monoagent/node: the official nodejs.org build, whose SHA-256 must match
the release's SHASUMS256.txt.asc, itself signed by a Node.js release key
pinned in monoagent. It never touches your shell config.

A suitable system Node always takes precedence: the managed one is then
only appended to PATH. When the system Node is missing or older than
` + nodemgr.MinVersion + `, the managed Node goes first on PATH for every process
monoagent starts — monomind and agent CLIs, but also the commands of
workflow exec nodes, where a bare "node" is then the managed one. The PATH
you had before is kept in $` + nodemgr.UserPathEnv + `.

Changes to ~/.monoagent/node are locked, so installs from the GUI and the
CLI at once run one after the other, and remove/update keep a version a
running process uses (detected on Linux; Windows refuses to delete it; on
macOS stop monoagent's processes first).`,
	}
	cmd.AddCommand(d.newNodejsStatusCmd(), d.newNodejsInstallCmd(), d.newNodejsUpdateCmd(), d.newNodejsRemoveCmd())
	return cmd
}

type nodejsStatus struct {
	MinVersion     string   `json:"min_version"`
	SystemPath     string   `json:"system_path,omitempty"`
	SystemVersion  string   `json:"system_version,omitempty"`
	SystemSuitable bool     `json:"system_suitable"`
	Managed        string   `json:"managed_version,omitempty"`
	ManagedPath    string   `json:"managed_path,omitempty"`
	Installed      []string `json:"installed"`
	Active         string   `json:"active"` // "system" | "managed" | "none"
}

func (d Deps) newNodejsStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show which Node.js monoagent uses",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m := nodemgr.New()
			st := nodejsStatus{MinVersion: nodemgr.MinVersion, Installed: []string{}, Active: "none"}
			if p, v, ok := m.SystemNode(cmd.Context()); ok {
				st.SystemPath, st.SystemVersion, st.SystemSuitable = p, v, nodemgr.Suitable(v)
			}
			if inst, err := m.Installed(); err == nil && inst != nil {
				st.Installed = inst
			}
			if v, ok := m.Current(); ok {
				st.Managed, st.ManagedPath = v, m.NodePath(v)
			}
			switch {
			case st.SystemSuitable:
				st.Active = "system"
			case st.Managed != "":
				st.Active = "managed"
			}

			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(st)
			}
			sys := "not found"
			if st.SystemPath != "" {
				sys = fmt.Sprintf("v%s (%s)", st.SystemVersion, st.SystemPath)
				if !st.SystemSuitable {
					sys += " — too old, need >= " + nodemgr.MinVersion
				}
			}
			managed := "not installed"
			if st.Managed != "" {
				managed = fmt.Sprintf("v%s (%s)", st.Managed, st.ManagedPath)
			}
			fmt.Fprintf(out, "system Node:  %s\nmanaged Node: %s\nin use:       %s\n", sys, managed, st.Active)
			if st.Active == "none" {
				fmt.Fprintln(out, "\nInstall one with: monoagentcli nodejs install")
			}
			return nil
		},
	}
}

func (d Deps) newNodejsInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install [version]",
		Short: "Download and activate a managed Node.js (default: latest LTS)",
		Long: `Downloads Node.js from nodejs.org into ~/.monoagent/node, verifies the
signed checksum list and the archive's SHA-256 against it, and makes it the
active managed version. [version] is
"lts" (default), a major ("24") or an exact version ("24.1.0").
With --json, progress is streamed as NDJSON like ` + "`doctor fix`" + `.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			want := ""
			if len(args) == 1 {
				want = args[0]
			}
			return d.runNodejsInstall(cmd, want, false)
		},
	}
}

func (d Deps) newNodejsUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Move the managed Node.js to the latest LTS and remove older versions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, ok := nodemgr.New().Current(); !ok {
				return clicore.NotFound("no managed Node.js installed — use: monoagentcli nodejs install")
			}
			return d.runNodejsInstall(cmd, "lts", true)
		},
	}
}

func (d Deps) runNodejsInstall(cmd *cobra.Command, want string, prune bool) error {
	out := cmd.OutOrStdout()
	progress := doctor.ProgressWriter(out, d.JSONOutput())
	m := nodemgr.New()
	v, err := m.Install(cmd.Context(), want, progress)
	if err == nil && prune {
		kept, perr := m.PruneVersions(v)
		for _, k := range kept {
			progress("kept an older version: " + k.Error())
		}
		if perr != nil {
			progress("could not remove older versions: " + perr.Error())
		}
	}
	return FinishStreamed(out, d.JSONOutput(), err, "managed Node.js v"+v+" is active")
}

// FinishStreamed ends a command whose progress was streamed with
// progressWriter: a final NDJSON done/error event in JSON mode, a summary
// line otherwise.
func FinishStreamed(out io.Writer, asJSON bool, err error, okMsg string) error {
	if asJSON {
		if err != nil {
			doctor.WriteFixEvent(out, doctor.FixEvent{Kind: "error", Message: err.Error()})
			return &clicore.Error{Code: clicore.ExitCodeFor(err), Msg: err.Error()}
		}
		doctor.WriteFixEvent(out, doctor.FixEvent{Kind: "done", Message: okMsg})
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "✓ %s\n", okMsg)
	return nil
}

func (d Deps) newNodejsRemoveCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove [version]",
		Short: "Remove the managed Node.js (one version, or everything incl. packages installed with it)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m := nodemgr.New()
			version := ""
			what := "the managed Node.js and every package installed with it (" + m.Root + ", " + m.NpmRoot + ")"
			if len(args) == 1 {
				version = args[0]
				what = "managed Node.js " + version
			}
			if !yes {
				if d.JSONOutput() || !d.StdinIsTerminal() {
					return clicore.InvalidInput("refusing to remove %s without --yes", what)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Remove %s? [y/N] ", what)
				ans, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
					return nil
				}
			}
			if err := m.Remove(version); err != nil {
				return err
			}
			if d.JSONOutput() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"removed": what})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ removed %s\n", what)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Don't ask for confirmation")
	return cmd
}
