package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/monomind"
)

// newAgentCmd exposes the local agent-runner surface: thin proxies over
// monomind's Agent Exec Protocol commands. mono-agent never learns
// agent-CLI wire formats — discovery, probing, and smoke tests all live in
// monomind (`agent scan` / `agent test`, protocol §6).
func (d Deps) newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Local AI agent runtimes (via the monomind engine)",
		Long: "Detect, install and smoke-test the AI agent CLIs on this machine — claude, codex, " +
			"kimicode, opencode, qwen, grok, crush, copilot, pi, and more — through the monomind engine. " +
			"monomind runs every chat turn and AI generation in mono-agent on one of these runtimes, " +
			"using the runtime's own login; mono-agent stores no API keys for text AI. " +
			"If monomind is missing, these commands print the install hint.",
	}
	cmd.AddCommand(
		d.newAgentScanCmd(),
		d.newAgentTestCmd(),
		d.newAgentInstallCmd(),
		d.newAgentValidateCmd(),
		d.newAgentRosterCmd(),
	)
	return cmd
}

func (d Deps) newAgentScanCmd() *cobra.Command {
	var installedOnly bool
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Detect installed AI agent runtimes",
		Example: `  monoagentcli agent scan
  monoagentcli agent scan --installed --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := monomind.Scan(cmd.Context()) // handshakes internally
			if err != nil {
				return err
			}
			agents := res.Agents
			if installedOnly {
				agents = res.Installed()
			}
			if d.JSONOutput() {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]interface{}{"v": res.V, "agents": agents})
			}
			if len(agents) == 0 {
				fmt.Println("No agent runtimes detected. Install monomind-visible agents (see `agent scan` without --installed).")
				return nil
			}
			table := d.NewTable(os.Stdout, []string{"Runtime", "Installed", "Version", "Binary"}, nil)
			for _, a := range agents {
				binary, version := "—", "—"
				if a.Binary != nil && *a.Binary != "" {
					binary = *a.Binary
				}
				if a.Version != nil && *a.Version != "" {
					version = *a.Version
				}
				installed := "no"
				if a.Installed {
					installed = "yes"
				}
				table.Append([]string{a.ID, installed, version, binary})
			}
			table.Render()
			if !installedOnly {
				fmt.Fprintln(os.Stderr, "\nNot installed:")
				for _, a := range res.Agents {
					if !a.Installed {
						fmt.Fprintf(os.Stderr, "  %s: %s\n", a.ID, a.InstallHint)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&installedOnly, "installed", false, "Only list installed runtimes")
	return cmd
}

func (d Deps) newAgentTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "test <runtime>",
		Short:   "Smoke-test one agent runtime (also verifies auth)",
		Args:    cobra.ExactArgs(1),
		Example: `  monoagentcli agent test codex`,
		RunE: func(cmd *cobra.Command, args []string) error {
			runtime := args[0]
			ctx := cmd.Context()
			bin, _, err := monomind.Ensure(ctx)
			if err != nil {
				return err
			}
			timeoutRaw, _ := cmd.Flags().GetString("timeout")
			timeout, err := parseDurationFlag(timeoutRaw)
			if err != nil {
				return err
			}
			res, err := monomind.Exec(ctx, monomind.ExecOptions{
				Bin:     bin,
				Runtime: runtime,
				Prompt:  "Reply with the single word: ok",
				Timeout: timeout,
				// Tested the way chat runs it.
				Sandbox:          monomind.TurnSandboxMode,
				WorkspacePurpose: monomind.WorkspaceAgentTest,
			}, (&turnPrinter{w: os.Stderr}).print)
			if err != nil {
				return err
			}
			if res.Err != nil {
				return res.Err
			}
			fmt.Fprintf(os.Stderr, "\n%s: OK (session %s)\n", runtime, res.SessionID)
			return nil
		},
	}
	cmd.Flags().String("timeout", "90s", "Overall timeout")
	return cmd
}

// turnPrinter writes one turn's events for a person to read. A runtime that
// streams (start's streams_incrementally) sends the reply in chunks, which
// are printed as running text; otherwise each assistant event is a whole
// message on its own line.
type turnPrinter struct {
	w           io.Writer
	incremental bool
	midLine     bool
}

func (p *turnPrinter) print(ev monomind.Event) {
	switch ev.Type {
	case monomind.EventStart:
		p.incremental = ev.StreamsIncrementally
	case monomind.EventAssistant:
		if !p.incremental {
			fmt.Fprintln(p.w, ev.Text)
			return
		}
		if ev.Text != "" {
			fmt.Fprint(p.w, ev.Text)
			p.midLine = !strings.HasSuffix(ev.Text, "\n")
		}
	case monomind.EventToolCall:
		p.endLine()
	case monomind.EventError:
		p.endLine()
		fmt.Fprintf(p.w, "error: %s: %s\n", ev.Code, ev.ErrMessage)
	case monomind.EventDone:
		p.endLine()
		fmt.Fprintf(p.w, "exit: %d\n", ev.ExitCode)
	}
}

// endLine finishes streamed text that did not end in a newline, so the next
// line starts at the left edge.
func (p *turnPrinter) endLine() {
	if p.midLine {
		fmt.Fprintln(p.w)
		p.midLine = false
	}
}
