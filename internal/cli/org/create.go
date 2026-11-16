package org

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/orgenv"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orgsign"
)

func (env *orgEnv) newOrgValidateCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "validate <name>",
		Short: "Validate an org's config against the runtime schema and structural rules",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			out, valErr := monomind.OrgValidate(cmd.Context(), root(), name)
			payload := map[string]interface{}{
				"v":     1,
				"org":   name,
				"valid": valErr == nil,
				// Non-fatal: never flips valid (C-36).
				"warnings": nonNilStrings(childGoalWarnings(root(), name)),
			}
			if valErr != nil {
				payload["error"] = valErr.Error()
			} else {
				payload["output"] = out
			}
			b, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			if err := printOrgJSON(b); err != nil {
				return err
			}
			if valErr != nil {
				// Exit 1 on an invalid org. The report above is the output:
				// main adds no second {"error"} for a reportedError.
				return env.d.Reported(fmt.Errorf("org %s is invalid: %w", name, valErr))
			}
			return nil
		},
	}
}

// newOrgCreateJSONCmd is the only CLI path that can set custom roles — `org
// create` (the real monomind binary) only scaffolds from a fixed set of
// templates, and monomind itself has no --project flag at all (every other
// org subcommand's --project support comes entirely from monoagentcli
// itself spawning monomind with the resolved directory as its cwd; a model
// with Bash access calling `monomind` directly, bypassing monoagentcli,
// gets none of that and silently resolves against its own actual cwd).
// This command sidesteps monomind's binary entirely for the write itself —
// it reuses the exact orgdesign.Doc/Save path the Wails GUI's own
// SaveOrgDesign already goes through (wails-app/app_orgs_design.go) — then
// still runs monomind's real `org validate` afterward so the response
// reflects the same schema/structural checks every other org gets.
func newOrgCreateJSONCmd(env *orgEnv) *cobra.Command {
	root := env.root
	var jsonBlob string
	c := &cobra.Command{
		Use:   "create-json <name>",
		Short: "Create or overwrite an org from a full JSON document (the only way to set custom roles from the CLI)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			var d orgdesign.Doc
			if err := json.Unmarshal([]byte(jsonBlob), &d); err != nil {
				return fmt.Errorf("invalid --json: %w", err)
			}
			if d.Name == "" {
				d.Name = name
			} else if d.Name != name {
				return fmt.Errorf("org name %q in --json does not match the <name> argument %q", d.Name, name)
			}
			var findings interface{}
			if db, profileID, profileRoot, perr := env.Profile(); perr == nil {
				_, statErr := os.Stat(filepath.Join(orgdesign.OrgsDir(profileRoot), name+".json"))
				if statErr == nil {
					// A replace of an existing org: its signature goes, whatever the loaded hash says.
					orgenv.DropSignature(profileRoot, name)
				}
				if os.IsNotExist(statErr) {
					if other, err := orgNameInUse(db.DB, name, profileID); err != nil {
						return err
					} else if other != "" {
						return env.d.InvalidInput("org name %q is already used in profile %q; org names are unique on this machine — try %s-%s", name, other, name, shortProfile(profileID))
					}
					if err := ensureNewOrgAutonomy(cmd.Context(), db, profileID, &d, "cli"); err != nil {
						return err
					}
				}
				// Grants, providers, and endpoints in the document only
				// survive when a row backs them (C-3).
				// A whole document can come from anyone, the chat assistant
				// included, so it is never signed here: the user reviews it
				// with `org sign`.
				rep, err := orgenv.SaveReconciled(cmd.Context(), db, profileID, profileRoot, &d, env.genOptions(profileID))
				if err != nil {
					return err
				}
				findings = rep.Findings
			} else if docCarriesEnforcedKeys(&d) {
				return fmt.Errorf("this document carries grants, tool providers, or automation roles, which need the org folder of the profile named by --profile: %w", perr)
			} else {
				orgenv.DropSignature(root(), name) // replacing an org drops its signature
				_, out, err := orgenv.SaveSigned(cmd.Context(), root(), name, &d)
				if err != nil {
					return err
				}
				orgenv.WarnSignature(out)
			}
			path, _ := orgdesign.ConfigPath(root(), name)
			sha, err := fileSHA256(path)
			if err != nil {
				return err
			}
			out, valErr := monomind.OrgValidate(cmd.Context(), root(), name)
			payload := map[string]interface{}{
				"v": 1, "org": name, "sha256": sha, "valid": valErr == nil,
				"warnings": nonNilStrings(childGoalWarnings(root(), name)),
			}
			if findings != nil {
				payload["reconcile"] = findings
			}
			if valErr != nil {
				payload["validate_error"] = valErr.Error()
			} else {
				payload["validate_output"] = out
			}
			b, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			return printOrgJSON(b)
		},
	}
	c.Flags().StringVar(&jsonBlob, "json", "", `Full org document JSON: {"name","goal","status","schedule","run_config":{...},"roles":[{"id","title","type","reports_to","responsibilities":[...],"policy":{...},...}]} — same shape as .monomind/orgs/<name>.json`)
	_ = c.MarkFlagRequired("json")
	return c
}

func newOrgReloadCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "reload <name>",
		Short: "Signal a running org's daemon to reload its config",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			out, reloadErr := monomind.OrgReload(cmd.Context(), root(), name)
			payload := map[string]interface{}{
				"v":        1,
				"org":      name,
				"reloaded": reloadErr == nil,
			}
			if reloadErr != nil {
				payload["error"] = reloadErr.Error()
			} else {
				payload["output"] = out
			}
			// monomind 2.21 keeps a running org on its last verified
			// definition until this one is signed: say so, with the fix.
			if orgenv.SigningOn(cmd.Context(), root()) {
				if raw, _, err := orgsign.ReadFile(root(), name); err == nil {
					if st := orgenv.SignStatus(cmd.Context(), root(), name, raw); st.Refused() {
						payload["signature"] = st
						payload["warning"] = orgsign.Message(name, st) + " — until then the running org keeps its last signed definition"
					}
				}
			}
			b, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			return printOrgJSON(b)
		},
	}
}
