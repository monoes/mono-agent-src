package jevcli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
)

type jevSurfaceStatus struct {
	Surface          string   `json:"surface"`
	Title            string   `json:"title"`
	Description      string   `json:"description"`
	Egress           []string `json:"egress"`
	Enabled          bool     `json:"enabled"`
	Threshold        float64  `json:"threshold"`
	DefaultThreshold float64  `json:"default_threshold"`
	// Provider is the surface's own provider choice for the profile, even
	// when it names the default; "" when the surface follows the default.
	Provider string `json:"provider,omitempty"`
	// ProviderPinned is true when the surface has a provider choice of its own.
	ProviderPinned bool `json:"provider_pinned,omitempty"`
	// SampledLowers is true when the surface lowers a threshold a sampling
	// provider cannot read (jevconf.LowersSampled).
	SampledLowers bool `json:"sampled_lowers_threshold,omitempty"`
}

type jevStatus struct {
	ProfileID string             `json:"profile_id"`
	KeySource string             `json:"key_source"` // config | vault | profile-vault | env | none
	KeyEntry  string             `json:"key_entry"`  // vault entry name when key_source is vault
	Model     string             `json:"model"`
	BaseURL   string             `json:"base_url"`
	Surfaces  []jevSurfaceStatus `json:"surfaces"`
	// The machine-default decision provider: its name, kind, API host and how
	// its probabilities are obtained (plan D7; "" until probed).
	Provider     string `json:"provider"`
	ProviderKind string `json:"provider_kind"`
	ProviderHost string `json:"provider_host"`
	Probability  string `json:"probability"`
	// For a default provider other than TypeSafe (key_source is TypeSafe's
	// key): where its key comes from (vault | keyless; nothing decrypted),
	// and why it cannot be used ("" when it can).
	ProviderKeySource string `json:"provider_key_source,omitempty"`
	ProviderError     string `json:"provider_error,omitempty"`
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
			st := jevStatus{ProfileID: d.ProfileID(), KeySource: "none", Model: jevModel(), BaseURL: jev.DefaultBaseURL}
			// KeySource never decrypts, so status never prompts for a keyring.
			if source, err := jevconf.KeySource(cmd.Context(), db.DB, d.ProfileID()); err == nil {
				st.KeySource = source
			}
			// What a client sending that key would call (a stored key never
			// goes to TYPESAFE_BASE_URL).
			if b, err := jevconf.BaseURL(cmd.Context(), st.KeySource); err == nil {
				st.BaseURL = b
			} else {
				st.BaseURL = "refused: " + err.Error()
			}
			if name, ok := jevconf.VaultKeyName(cmd.Context(), db.DB); ok {
				st.KeyEntry = name
			}
			st.Provider = jevconf.DefaultProvider(db.DB)
			p, known, err := jevconf.FindProvider(db.DB, st.Provider)
			if err == nil && known {
				st.ProviderKind, st.ProviderHost, st.Probability = p.Kind, hostOf(p.BaseURL), string(p.Caps.Probability)
				if p.Builtin() && !strings.HasPrefix(st.BaseURL, "refused: ") {
					st.ProviderHost = hostOf(st.BaseURL)
				}
			}
			if st.Provider != jev.ProviderTypeSafe {
				// Can the default provider be used? Its vault entry is listed, never read.
				if src, err := jevconf.ProviderKeySourceOf(cmd.Context(), db.DB, d.ProfileID(), st.Provider); err != nil {
					st.ProviderError = err.Error()
				} else {
					st.ProviderKeySource = src
				}
			}
			overrides := false
			for _, s := range jevconf.Surfaces {
				def := jevconf.DefaultThreshold[s]
				ss := jevSurfaceStatus{
					Surface:          string(s),
					Title:            jevconf.Describe[s].Title,
					Description:      jevconf.Describe[s].Description,
					Egress:           jevconf.Egress[s],
					Enabled:          jevconf.Enabled(db.DB, d.ProfileID(), s),
					Threshold:        jevconf.Threshold(db.DB, d.ProfileID(), s, def),
					DefaultThreshold: def,
					SampledLowers:    jevconf.LowersSampled(s),
				}
				if own, pinned := jevconf.SurfaceProviderSetting(db.DB, d.ProfileID(), s); pinned {
					ss.Provider, ss.ProviderPinned, overrides = own, true, true
				}
				st.Surfaces = append(st.Surfaces, ss)
			}
			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				return writeJevJSON(out, st)
			}
			key := st.KeySource
			if key == "none" {
				key = "none — store one with `monoagentcli jev key set` (key on stdin) or set TYPESAFE_API_KEY"
			}
			fmt.Fprintf(out, "Profile: %s\nKey:     %s\nModel:   %s\nAPI:     %s\n", st.ProfileID, key, st.Model, st.BaseURL)
			// With TypeSafe the default, the text is what it was before providers.
			switch {
			case st.Provider == jev.ProviderTypeSafe:
			case !known:
				fmt.Fprintf(out, "Provider: %s (unknown provider: it is not among the configured ones; `jev provider list` shows them, "+
					"`jev provider use typesafe` restores TypeSafe)\n", st.Provider)
			default:
				prob := st.Probability
				if prob == "" {
					prob = "not probed"
				}
				fmt.Fprintf(out, "Provider: %s (%s at %s, probability %s)\n", st.Provider, st.ProviderKind, st.ProviderHost, prob)
				if st.ProviderError != "" {
					fmt.Fprintf(out, "          cannot be used: %s\n", st.ProviderError)
				}
			}
			fmt.Fprintln(out)
			header := []string{"Surface", "Enabled", "Threshold"}
			if overrides {
				header = append(header, "Provider")
			}
			table := clicore.NewPlainTable(out, header, nil)
			for _, s := range st.Surfaces {
				on := "no"
				if s.Enabled {
					on = "yes"
				}
				row := []string{s.Surface, on, strconv.FormatFloat(s.Threshold, 'f', -1, 64)}
				if overrides {
					prov := s.Provider + " (pinned)"
					if !s.ProviderPinned {
						prov = st.Provider
					}
					row = append(row, prov)
				}
				table.Append(row)
			}
			table.Render()
			for _, note := range sampledNotes(db.DB, st) {
				fmt.Fprintln(out, note)
			}
			return nil
		},
	}
}
