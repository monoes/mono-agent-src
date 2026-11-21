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
	// Provider is the surface's own provider when it is not the default.
	Provider string `json:"provider,omitempty"`
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
			if p, ok, err := jevconf.FindProvider(db.DB, st.Provider); err == nil && ok {
				st.ProviderKind, st.ProviderHost, st.Probability = p.Kind, hostOf(p.BaseURL), string(p.Caps.Probability)
				if p.Builtin() && !strings.HasPrefix(st.BaseURL, "refused: ") {
					st.ProviderHost = hostOf(st.BaseURL)
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
				}
				if ap := jevconf.ActiveProvider(db.DB, d.ProfileID(), s); ap != st.Provider {
					ss.Provider, overrides = ap, true
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
			if st.Provider != jev.ProviderTypeSafe {
				prob := st.Probability
				if prob == "" {
					prob = "not probed"
				}
				fmt.Fprintf(out, "Provider: %s (%s at %s, probability %s)\n", st.Provider, st.ProviderKind, st.ProviderHost, prob)
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
					prov := s.Provider
					if prov == "" {
						prov = st.Provider
					}
					row = append(row, prov)
				}
				table.Append(row)
			}
			table.Render()
			return nil
		},
	}
}
