package jevcli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/secrets"
)

// NewCommand returns the `jev` command group: TypeSafe Jev, an optional
// non-generative decision provider (it only picks among options the code
// enumerates). Every implicit surface is off until `jev enable`.
func NewCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jev",
		Short: "TypeSafe Jev decisions: key status, per-surface opt-in, usage",
		Long: "TypeSafe Jev answers typed questions (pick one option, yes/no, score) about\n" +
			"a JSON state; it never writes text. monoagent uses it only where code picks\n" +
			"from a closed set of options, and every implicit surface is off until\n" +
			"`jev enable <surface>` for the profile.\n" +
			"\n" +
			"Key: the machine-wide vault, shared by all profiles (`jev key set`, key on stdin; a key in a profile's vault still works as a fallback; `jev key adopt` copies it), else TYPESAFE_API_KEY. TYPESAFE_DEFAULT_MODEL and\n" +
			"TYPESAFE_BASE_URL override the model and API host.",
	}
	cmd.AddCommand(
		newJevStatusCmd(d),
		newJevKeyCmd(d),
		newJevEnableCmd(d),
		newJevDisableCmd(d),
		newJevUsageCmd(d),
		newJevAskCmd(d),
		newJevModelsCmd(d),
	)
	return cmd
}

func writeJevJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func jevModel() string {
	if m := os.Getenv("TYPESAFE_DEFAULT_MODEL"); m != "" {
		return m
	}
	return jev.DefaultModel
}

func jevBaseURL() string {
	if b := os.Getenv("TYPESAFE_BASE_URL"); b != "" {
		return strings.TrimRight(b, "/")
	}
	return jev.DefaultBaseURL
}

func jevSurfaceNames() string {
	names := make([]string, len(jevconf.Surfaces))
	for i, s := range jevconf.Surfaces {
		names[i] = string(s)
	}
	return strings.Join(names, ", ")
}

func parseJevSurface(arg string) (jevconf.Surface, error) {
	s := jevconf.Surface(arg)
	if !jevconf.Known(s) {
		return "", clicore.InvalidInput("unknown surface %q; valid surfaces: %s", arg, jevSurfaceNames())
	}
	return s, nil
}

// jevKeyErr classifies a key-resolution or client error: a missing key is
// an auth failure (exit 4).
func jevKeyErr(err error) error {
	if errors.Is(err, jev.ErrNoAPIKey) {
		return clicore.AuthConnection("%v — store one with `monoagentcli jev key set` (key on stdin) or set TYPESAFE_API_KEY", err)
	}
	if errors.Is(err, jevconf.ErrKeyUnreadable) {
		return clicore.AuthConnection("%v — check that the OS keyring or vault passphrase is available; the key itself is stored", err)
	}
	return err
}

type jevSurfaceStatus struct {
	Surface          string   `json:"surface"`
	Title            string   `json:"title"`
	Description      string   `json:"description"`
	Egress           []string `json:"egress"`
	Enabled          bool     `json:"enabled"`
	Threshold        float64  `json:"threshold"`
	DefaultThreshold float64  `json:"default_threshold"`
}

type jevStatus struct {
	ProfileID string             `json:"profile_id"`
	KeySource string             `json:"key_source"` // config | vault | profile-vault | env | none
	KeyEntry  string             `json:"key_entry"`  // vault entry name when key_source is vault
	Model     string             `json:"model"`
	BaseURL   string             `json:"base_url"`
	Surfaces  []jevSurfaceStatus `json:"surfaces"`
}

func newJevStatusCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show where the key comes from, the model, and each surface's state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			st := jevStatus{ProfileID: d.ProfileID(), KeySource: "none", Model: jevModel(), BaseURL: jevBaseURL()}
			// KeySource never decrypts, so status never prompts for a keyring.
			if source, err := jevconf.KeySource(cmd.Context(), db.DB, d.ProfileID()); err == nil {
				st.KeySource = source
			}
			if name, ok := jevconf.VaultKeyName(cmd.Context(), db.DB); ok {
				st.KeyEntry = name
			}
			for _, s := range jevconf.Surfaces {
				def := jevconf.DefaultThreshold[s]
				st.Surfaces = append(st.Surfaces, jevSurfaceStatus{
					Surface:          string(s),
					Title:            jevconf.Describe[s].Title,
					Description:      jevconf.Describe[s].Description,
					Egress:           jevconf.Egress[s],
					Enabled:          jevconf.Enabled(db.DB, d.ProfileID(), s),
					Threshold:        jevconf.Threshold(db.DB, d.ProfileID(), s, def),
					DefaultThreshold: def,
				})
			}
			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				return writeJevJSON(out, st)
			}
			key := st.KeySource
			if key == "none" {
				key = "none — store one with `monoagentcli jev key set` (key on stdin) or set TYPESAFE_API_KEY"
			}
			fmt.Fprintf(out, "Profile: %s\nKey:     %s\nModel:   %s\nAPI:     %s\n\n", st.ProfileID, key, st.Model, st.BaseURL)
			table := clicore.NewPlainTable(out, []string{"Surface", "Enabled", "Threshold"}, nil)
			for _, s := range st.Surfaces {
				on := "no"
				if s.Enabled {
					on = "yes"
				}
				table.Append([]string{s.Surface, on, strconv.FormatFloat(s.Threshold, 'f', -1, 64)})
			}
			table.Render()
			return nil
		},
	}
}

func newJevEnableCmd(d Deps) *cobra.Command {
	var threshold float64
	var yes bool
	cmd := &cobra.Command{
		Use:   "enable <surface>",
		Short: "Let Jev decide or suggest on one surface for this profile",
		Long: "Switch one surface on for the profile named by --profile. The data that surface sends\n" +
			"to TypeSafe is printed first; confirm at the prompt, or pass --yes (required\n" +
			"when stdin is not a terminal).\n\nSurfaces: " + jevSurfaceNames(),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := parseJevSurface(args[0])
			if err != nil {
				return err
			}
			if why := d.Refusal(); why != "" {
				return clicore.OperatorOnly("jev enable %s sends this profile's data to TypeSafe, which only the user switches on: %s; run it yourself in your terminal or the app", s, why)
			}
			setThreshold := cmd.Flags().Changed("threshold")
			if setThreshold && (threshold <= 0 || threshold > 1) {
				return clicore.InvalidInput("--threshold %v must be in (0,1]", threshold)
			}
			out := cmd.OutOrStdout()
			notes := out
			if d.JSONOutput() {
				notes = cmd.ErrOrStderr()
			}
			fmt.Fprintf(notes, "This sends to TypeSafe (surface %s):\n", s)
			for _, e := range jevconf.Egress[s] {
				fmt.Fprintf(notes, "  - %s\n", e)
			}
			if !yes {
				if d.JSONOutput() || !d.StdinIsTerminal() {
					return clicore.InvalidInput("enabling %s needs --yes when stdin is not a terminal", s)
				}
				fmt.Fprintf(out, "Enable Jev for %s? [y/N] ", s)
				ans, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
					fmt.Fprintln(out, "Not enabled.")
					return nil
				}
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			if setThreshold {
				if err := jevconf.SetThreshold(db.DB, d.ProfileID(), s, threshold); err != nil {
					return err
				}
			}
			if err := jevconf.SetEnabled(db.DB, d.ProfileID(), s, true); err != nil {
				return err
			}
			eff := jevconf.Threshold(db.DB, d.ProfileID(), s, jevconf.DefaultThreshold[s])
			if _, _, err := jevconf.ResolveKey(cmd.Context(), db.DB, d.ProfileID(), ""); errors.Is(err, jev.ErrNoAPIKey) {
				fmt.Fprintln(cmd.ErrOrStderr(), "note: no TypeSafe key yet, so the surface stays inactive — store one with `monoagentcli jev key set` or set TYPESAFE_API_KEY")
			}
			if d.JSONOutput() {
				return writeJevJSON(out, map[string]any{"profile_id": d.ProfileID(), "surface": string(s),
					"enabled": true, "threshold": eff, "egress": jevconf.Egress[s]})
			}
			fmt.Fprintf(out, "Jev enabled for %s (profile %s, threshold %s).\n", s, d.ProfileID(), strconv.FormatFloat(eff, 'f', -1, 64))
			return nil
		},
	}
	cmd.Flags().Float64Var(&threshold, "threshold", 0, "Gate value in (0,1] the top option's probability must reach (default: the surface's default)")
	cmd.Flags().BoolVar(&yes, "yes", false, "Enable without asking (required when stdin is not a terminal)")
	return cmd
}

func newJevDisableCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "disable <surface>",
		Short: "Switch one surface off for this profile (today's behaviour returns)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := parseJevSurface(args[0])
			if err != nil {
				return err
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			if err := jevconf.SetEnabled(db.DB, d.ProfileID(), s, false); err != nil {
				return err
			}
			if d.JSONOutput() {
				return writeJevJSON(cmd.OutOrStdout(), map[string]any{"profile_id": d.ProfileID(), "surface": string(s), "enabled": false})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Jev disabled for %s (profile %s).\n", s, d.ProfileID())
			return nil
		},
	}
}

// parseJevSince accepts Go durations (24h, 90m) plus whole days (7d).
func parseJevSince(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	var d time.Duration
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
	}
	if d <= 0 {
		return 0, fmt.Errorf("duration %q must be positive", s)
	}
	return d, nil
}

func newJevUsageCmd(d Deps) *cobra.Command {
	var since string
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Calls, input tokens and estimated cost by surface",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			window, err := parseJevSince(since)
			if err != nil {
				return clicore.InvalidInput("--since: %v (use e.g. 24h, 7d, 30d)", err)
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			from := time.Now().Add(-window)
			rows, err := jevconf.UsageSince(db.DB, d.ProfileID(), from)
			if err != nil {
				return err
			}
			total := jevconf.Usage{Surface: "total"}
			var latencySum int
			for _, u := range rows {
				total.Calls += u.Calls
				total.Failures += u.Failures
				total.InputTokens += u.InputTokens
				total.EstimatedUSD += u.EstimatedUSD
				latencySum += u.AvgLatencyMS * u.Calls
			}
			if total.Calls > 0 {
				total.AvgLatencyMS = latencySum / total.Calls
			}
			if rows == nil {
				rows = []jevconf.Usage{}
			}
			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				return writeJevJSON(out, map[string]any{"profile_id": d.ProfileID(),
					"since": from.UTC().Format(time.RFC3339), "surfaces": rows, "total": total})
			}
			if len(rows) == 0 {
				fmt.Fprintf(out, "No Jev calls since %s.\n", from.Local().Format("2006-01-02 15:04"))
				return nil
			}
			table := clicore.NewPlainTable(out, []string{"Surface", "Calls", "Failures", "Input tokens", "Est. USD", "Avg latency"}, nil)
			for _, u := range append(rows, total) {
				table.Append([]string{u.Surface, strconv.Itoa(u.Calls), strconv.Itoa(u.Failures), strconv.Itoa(u.InputTokens),
					fmt.Sprintf("$%.6f", u.EstimatedUSD), fmt.Sprintf("%d ms", u.AvgLatencyMS)})
			}
			table.Render()
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "7d", "Window to report (e.g. 24h, 7d, 30d)")
	return cmd
}

// jevAskRequest is the `jev ask` body.
type jevAskRequest struct {
	State     any                     `json:"state"`
	Questions map[string]jev.Question `json:"questions"`
	Model     string                  `json:"model,omitempty"`
}

func newJevAskCmd(d Deps) *cobra.Command {
	var reqPath string
	cmd := &cobra.Command{
		Use:   "ask",
		Short: "Send one raw request ({state, questions, model?}) and print the answers",
		Long: "Send one raw /v1/systemone request for debugging or from an agent. The body\n" +
			"is {\"state\": …, \"questions\": {id: {type, criteria, instructions}}, \"model\"?}\n" +
			"read from --request <file> or `--request -` (stdin). Answers are validated\n" +
			"before printing; the call is recorded in usage as surface \"cli\".",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if reqPath == "" {
				return clicore.InvalidInput("--request <file> (or - for stdin) is required")
			}
			var raw []byte
			var err error
			if reqPath == "-" {
				secrets.MarkStdinConsumed()
				raw, err = io.ReadAll(io.LimitReader(cmd.InOrStdin(), 16<<20))
			} else {
				raw, err = os.ReadFile(reqPath)
			}
			if err != nil {
				return clicore.InvalidInput("reading request: %v", err)
			}
			var req jevAskRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				return clicore.InvalidInput("request is not valid JSON: %v", err)
			}
			if len(req.Questions) == 0 {
				return clicore.InvalidInput("request has no questions")
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			c, err := jevconf.NewClient(cmd.Context(), db.DB, d.ProfileID(), "", req.Model, jevconf.CLI)
			if err != nil {
				return jevKeyErr(err)
			}
			resp, err := c.Ask(cmd.Context(), req.State, req.Questions)
			if err != nil {
				return err
			}
			return writeJevJSON(cmd.OutOrStdout(), map[string]any{"model": resp.Model, "answers": resp.Answers,
				"usage": resp.Usage, "latency_ms": resp.LatencyMS})
		},
	}
	cmd.Flags().StringVar(&reqPath, "request", "", "Request JSON file, or - for stdin")
	return cmd
}

func newJevModelsCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "models",
		Short: "List the Jev models the key can use",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			key, _, err := jevconf.ResolveKey(cmd.Context(), db.DB, d.ProfileID(), "")
			if err != nil {
				return jevKeyErr(err)
			}
			c, err := jev.NewClient(key, "")
			if err != nil {
				return jevKeyErr(err)
			}
			models, err := c.Models(cmd.Context())
			if err != nil {
				return clicore.AuthConnection("%v", err)
			}
			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				return writeJevJSON(out, map[string]any{"models": models})
			}
			for _, m := range models {
				fmt.Fprintln(out, m.ID)
			}
			return nil
		},
	}
}
