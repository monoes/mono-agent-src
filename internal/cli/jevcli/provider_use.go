package jevcli

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
)

// providerProber is what an adapter whose capabilities are found by asking
// the server exports (openaicompat and the others).
type providerProber interface {
	Probe(ctx context.Context) (jev.Capabilities, error)
}

// probeTimeout bounds one probe.
const probeTimeout = 30 * time.Second

// probeResult is `jev provider probe`'s output.
type probeResult struct {
	OK        bool             `json:"ok"`
	Caps      jev.Capabilities `json:"caps"`
	LatencyMS int64            `json:"latency_ms"`
	Error     string           `json:"error"`
}

// runProbe checks provider p answers. An adapter with a Probe finds its
// capabilities, which are saved onto its record; any other backend (and
// TypeSafe) is asked one tiny yes/no question and reports its static
// capabilities. A save that fails is noted on notes, never fails the probe.
func runProbe(ctx context.Context, db *sql.DB, profileID string, p jevconf.Provider, notes io.Writer) probeResult {
	res := probeResult{Caps: p.Caps}
	defer func() {
		if res.Caps.Kinds == nil {
			res.Caps.Kinds = []string{}
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	c, _, err := jevconf.ResolveProviderClient(ctx, db, profileID, p.Name, "", "")
	if err != nil {
		res.Error = err.Error()
		return res
	}
	started := time.Now()
	if pr, ok := c.Backend.(providerProber); ok {
		caps, err := pr.Probe(ctx)
		res.LatencyMS = time.Since(started).Milliseconds()
		if err != nil {
			res.Error = err.Error()
			return res
		}
		res.OK, res.Caps = true, caps
		if !p.Builtin() {
			p.Caps = caps
			if err := jevconf.SaveProvider(ctx, db, p); err != nil {
				fmt.Fprintf(notes, "note: the capabilities were not saved: %v\n", err)
			}
		}
		return res
	}
	_, err = c.Ask(ctx, map[string]any{"probe": "monoagent checks this provider answers"},
		map[string]jev.Question{"ok": {Type: jev.TypeNoul, Instructions: "Answer yes."}})
	res.LatencyMS = time.Since(started).Milliseconds()
	if c.Backend != nil {
		res.Caps = c.Backend.Capabilities()
	} else {
		res.Caps = jevconf.TypeSafeProvider().Caps
	}
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.OK = true
	return res
}

func newJevProviderProbeCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "probe <name>",
		Short: "Check a provider answers, and record what it can do",
		Long: "Asks the provider what it can do (or one tiny yes/no question) and saves the\n" +
			"capabilities found on its record. With --json the result is always printed with\n" +
			"exit 0: {ok, caps, latency_ms, error}.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			p, err := findProvider(db.DB, args[0])
			if err != nil {
				return err
			}
			res := runProbe(cmd.Context(), db.DB, d.ProfileID(), p, cmd.ErrOrStderr())
			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				return writeJevJSON(out, res)
			}
			if !res.OK {
				return clicore.AuthConnection("provider %q: %s", p.Name, res.Error)
			}
			fmt.Fprintf(out, "Provider %q answers (%d ms): probability %s, questions %s.\n",
				p.Name, res.LatencyMS, res.Caps.Probability, strings.Join(res.Caps.Kinds, ", "))
			return nil
		},
	}
}

// destination names where a provider's data goes, for consent texts.
func destination(p jevconf.Provider) string {
	if p.Builtin() {
		return "TypeSafe at " + hostOf(p.BaseURL)
	}
	return fmt.Sprintf("provider %q at %s", p.Name, hostOf(p.BaseURL))
}

// affectedSurface is one enabled surface whose data a provider switch moves.
type affectedSurface struct {
	profile string
	surface jevconf.Surface
}

// followers is what a provider switch moves to the new provider.
type followers struct {
	surfaces []affectedSurface
	// deciders are the org deciders of kind jev that follow the default, as
	// "<profile>/<org>".
	deciders []string
	// everyDefaultUser: the switch is of the machine default, which every
	// decision-model workflow node and dynamic-org chat without a provider
	// of its own follows (they are not enumerated).
	everyDefaultUser bool
}

func (f followers) empty() bool {
	return len(f.surfaces) == 0 && len(f.deciders) == 0 && !f.everyDefaultUser
}

// followersOfDefault lists, over every profile, what follows the machine
// default current: the enabled surfaces and the jev org deciders with no
// other choice of their own, and (always) the nodes and chats that use the
// default.
func followersOfDefault(ctx context.Context, db *sql.DB, current string) (followers, error) {
	enabled, err := enabledSurfaces(db)
	if err != nil {
		return followers{}, err
	}
	f := followers{everyDefaultUser: true}
	for _, a := range enabled {
		if jevconf.ActiveProvider(db, a.profile, a.surface) == current {
			f.surfaces = append(f.surfaces, a)
		}
	}
	if f.deciders, err = jevconf.DeciderFollowers(ctx, db); err != nil {
		return followers{}, err
	}
	return f, nil
}

// What the followers a surface list does not cover send (consent).
var (
	deciderEgress = []string{
		"the pending item's kind, class, tier and summary, the requester's role, title and responsibilities",
		"the org's name and goal, the operator policy and earlier decisions",
		"text agents wrote: the request, its inputs and recent events (each cut to 6,000 characters)",
	}
	nodesLine  = "every workflow node that uses a decision model without naming a provider (ai.choose, ai.decide, browser.jev, Human-in-Loop auto_decide, applications.evaluate with runtime jev): the items and page text it judges"
	dynorgLine = "dynamic-org coder chats that ask Jev to staff their workers (surface dynorg), in every profile that does not choose a provider for it: the task and the candidate roles"
)

// enabledSurfaces lists every profile's enabled surfaces, from the settings
// keys jev.<profile>.<surface>.enabled (Enabled confirms each).
func enabledSurfaces(db *sql.DB) ([]affectedSurface, error) {
	rows, err := db.Query(`SELECT key FROM settings WHERE key LIKE 'jev.%.enabled' ORDER BY key`)
	if err != nil {
		return nil, err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, key)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []affectedSurface
	for _, key := range keys {
		rest := strings.TrimPrefix(key, "jev.")
		for _, s := range jevconf.Surfaces {
			if pid, ok := strings.CutSuffix(rest, "."+string(s)+".enabled"); ok && pid != "" && jevconf.Enabled(db, pid, s) {
				out = append(out, affectedSurface{pid, s})
			}
		}
	}
	return out, nil
}

// consent prints what a switch to p sends where and asks, mirroring `jev
// enable`: --yes, or a terminal to answer at. ok false means declined.
func consent(cmd *cobra.Command, d Deps, p jevconf.Provider, affected followers, yes bool) (ok bool, err error) {
	out := cmd.OutOrStdout()
	notes := out
	if d.JSONOutput() {
		notes = cmd.ErrOrStderr()
	}
	fmt.Fprintf(notes, "This sends to %s:\n", destination(p))
	for _, a := range affected.surfaces {
		fmt.Fprintf(notes, "  profile %s, surface %s:\n", a.profile, a.surface)
		for _, e := range jevconf.Egress[a.surface] {
			fmt.Fprintf(notes, "    - %s\n", e)
		}
	}
	for _, o := range affected.deciders {
		fmt.Fprintf(notes, "  org decider of %s:\n", o)
		for _, e := range deciderEgress {
			fmt.Fprintf(notes, "    - %s\n", e)
		}
	}
	if affected.everyDefaultUser {
		fmt.Fprintf(notes, "  %s\n  %s\n", nodesLine, dynorgLine)
	}
	return confirm(cmd, d, "switching to provider "+p.Name, "Use "+p.Name+"?", yes)
}

// confirm asks a yes/no question after a consent text: --yes, or a
// terminal to answer at. ok false means declined.
func confirm(cmd *cobra.Command, d Deps, what, question string, yes bool) (ok bool, err error) {
	out := cmd.OutOrStdout()
	if yes {
		return true, nil
	}
	if d.JSONOutput() || !d.StdinIsTerminal() {
		return false, clicore.InvalidInput("%s needs --yes when stdin is not a terminal", what)
	}
	fmt.Fprintf(out, "%s [y/N] ", question)
	ans, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
		fmt.Fprintln(out, "Not changed.")
		return false, nil
	}
	return true, nil
}

func newJevProviderUseCmd(d Deps) *cobra.Command {
	var surface string
	var yes, clear bool
	cmd := &cobra.Command{
		Use:   "use [<name>]",
		Short: "Make a provider the machine default, or one surface's provider for this profile",
		Long: "Without --surface, makes <name> the machine-wide default (`use typesafe` restores\n" +
			"TypeSafe). With --surface S, makes it S's provider for the profile named by --profile;\n" +
			"`--surface S --clear` drops that choice, so S follows the default again. A provider\n" +
			"whose probed question kinds lack one S asks is refused for S. Making a provider that\n" +
			"is not on this machine the default always prints what follows the default (enabled\n" +
			"surfaces, org deciders of kind jev, workflow nodes and chats without a provider of\n" +
			"their own) and asks; for --surface S it asks when S is enabled. Confirm at the\n" +
			"prompt, or pass --yes (required when stdin is not a terminal). " + clicore.OperatorOnlyHelp + "\n\nSurfaces: " + jevSurfaceNames(),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if why := clicore.OperatorRefusal(d.Refusal); why != "" {
				return clicore.OperatorOnly("jev provider use decides where Jev sends this machine's data, which only the user does: %s; run it yourself in your terminal or the app", why)
			}
			name := ""
			switch {
			case clear && len(args) > 0:
				return clicore.InvalidInput("--clear takes no provider name")
			case clear && surface == "":
				name = jev.ProviderTypeSafe // clearing the default restores TypeSafe
			case !clear && len(args) == 0:
				return clicore.InvalidInput("name a provider (`jev provider list`), or pass --surface S --clear")
			case len(args) == 1:
				name = args[0]
			}
			var s jevconf.Surface
			if surface != "" {
				var err error
				if s, err = parseJevSurface(surface); err != nil {
					return err
				}
				// The command is machine-wide; a surface choice belongs to one profile,
				// which must be named, not defaulted.
				if f := cmd.Flags().Lookup("profile"); f != nil && !f.Changed {
					return clicore.InvalidInput("--surface sets the provider of one profile's surface: pass --profile <id|name>")
				}
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			ctx := cmd.Context()
			current := jevconf.DefaultProvider(db.DB)
			var affected followers
			if s == "" {
				if affected, err = followersOfDefault(ctx, db.DB, current); err != nil {
					return err
				}
			} else {
				current = jevconf.ActiveProvider(db.DB, d.ProfileID(), s)
				if jevconf.Enabled(db.DB, d.ProfileID(), s) {
					affected.surfaces = []affectedSurface{{d.ProfileID(), s}}
				}
				if clear {
					name = jevconf.DefaultProvider(db.DB)
				}
			}
			target, err := findProvider(db.DB, name)
			if err != nil {
				return err
			}
			if s != "" && !clear {
				if err := jevconf.CheckSurfaceKinds(target, s); err != nil {
					return clicore.InvalidInput("%v", err)
				}
			}
			// D10: moving data to another machine asks first. A switch of the
			// machine default always does (workflow nodes and chats follow it).
			if target.Name != current && !jevconf.Loopback(target.BaseURL) && !affected.empty() {
				if ok, err := consent(cmd, d, target, affected, yes); !ok || err != nil {
					return err
				}
			}
			res := map[string]any{"scope": "default", "surface": "", "provider": target.Name, "cleared": clear}
			if s == "" {
				err = jevconf.SetDefaultProvider(ctx, db.DB, target.Name)
			} else {
				res["scope"], res["surface"] = "surface", string(s)
				choice := target.Name
				if clear {
					choice = ""
				}
				err = jevconf.SetSurfaceProvider(ctx, db.DB, d.ProfileID(), s, choice)
			}
			if err != nil {
				return jevconfErr(err)
			}
			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				return writeJevJSON(out, res)
			}
			switch {
			case s == "":
				fmt.Fprintf(out, "Default decision provider: %s (%s).\n", target.Name, hostOf(target.BaseURL))
			case clear:
				fmt.Fprintf(out, "Surface %s (profile %s) follows the default provider again (%s).\n", s, d.ProfileID(), target.Name)
			default:
				fmt.Fprintf(out, "Surface %s (profile %s) now uses provider %s (%s).\n", s, d.ProfileID(), target.Name, hostOf(target.BaseURL))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&surface, "surface", "", "Set this surface's provider for the profile instead of the machine default")
	cmd.Flags().BoolVar(&clear, "clear", false, "With --surface: drop the surface's own choice (it follows the default)")
	cmd.Flags().BoolVar(&yes, "yes", false, "Switch without asking (required when stdin is not a terminal)")
	return cmd
}
