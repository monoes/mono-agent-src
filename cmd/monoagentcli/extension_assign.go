package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/extension"
)

// Which browsers run which profiles. The profile sets live in each browser's
// extension; these helpers compute the new sets and ask the extensions to
// store them. `extension assign` is the "this profile runs in that browser
// and nowhere else" move the desktop app's Settings → Browsers makes.

func newExtensionAssignCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "assign <profile> [browser]",
		Short: "Run a profile in one browser only (or in no browser of its own)",
		Long: "<profile> is a profile id or name; <browser> is an instance id (or its first 4+\n" +
			"characters) or a label from `extension browsers`. The profile is removed from every\n" +
			"other browser and added to <browser>; other profiles in <browser> are left alone, so\n" +
			"several profiles can share one. With no <browser> the profile is removed everywhere,\n" +
			"i.e. it uses the default browser again. A browser that is not attached cannot be\n" +
			"changed; the bridge reports the browsers that are.",
		Example: "  monoagentcli extension assign Work \"Edge Work\"\n  monoagentcli extension assign Work",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			profileID, err := profileIDFor(cfg, args[0])
			if err != nil {
				return err
			}
			browser := ""
			if len(args) == 2 {
				browser = args[1]
			}
			return runExtensionAssign(cmd.Context(), cfg, cmd.OutOrStdout(), profileID, browser)
		},
	}
}

// bindingChange is one browser whose set has to change.
type bindingChange struct {
	Browser extension.ConnInfo
	Set     []string
}

// planAssign works out which browsers change so that profileID runs only in
// target (nil: nowhere). The target comes first, so the profile never has
// a gap with no browser. Legacy browsers cannot hold a binding and are left
// alone; a browser whose set is not fully known is an error rather than a
// guess that would drop a profile.
func planAssign(infos []extension.ConnInfo, profileID string, target *extension.ConnInfo) ([]bindingChange, error) {
	var plan []bindingChange
	if target != nil {
		set, err := knownSet(*target)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(set, profileID) {
			plan = append(plan, bindingChange{*target, addProfile(set, profileID)})
		}
	}
	for _, b := range infos {
		if b.Legacy || (target != nil && b.Instance == target.Instance) || !b.RunsProfile(profileID) {
			continue
		}
		set, err := knownSet(b)
		if err != nil {
			return nil, err
		}
		plan = append(plan, bindingChange{b, removeProfile(set, profileID)})
	}
	return plan, nil
}

// assignReport is what `extension assign --json` prints.
type assignReport struct {
	ProfileID string       `json:"profile_id"`
	Instance  string       `json:"instance,omitempty"`
	Changed   []browserRow `json:"changed"`
}

func runExtensionAssign(ctx context.Context, cfg *globalConfig, out io.Writer, profileID, query string) error {
	sender, infos, err := bridgeBrowsers()
	if err != nil {
		return err
	}
	var target *extension.ConnInfo
	if strings.TrimSpace(query) != "" {
		b, err := matchBrowser(infos, query)
		if err != nil {
			return errNotFound("browser %q is not attached to the bridge (attached: %s) — open it with the MonoAgent Bridge extension, then try again",
				query, attachedNames(infos))
		}
		target = &b
	}
	plan, err := planAssign(infos, profileID, target)
	if err != nil {
		return err
	}
	for _, ch := range plan {
		if err := setBrowserProfiles(sender, ch.Browser, ch.Set); err != nil {
			return err
		}
	}
	report := assignReport{ProfileID: profileID, Changed: []browserRow{}}
	if target != nil {
		report.Instance = target.Instance
	}
	if len(plan) > 0 {
		after, err := sender.Browsers()
		if err != nil {
			return errAuthConnection("listing browsers: %v", err)
		}
		changed := map[string]bool{}
		for _, ch := range plan {
			changed[ch.Browser.Instance] = true
		}
		for _, row := range browserRows(after, profileNames(ctx, cfg)) {
			if changed[row.Instance] {
				report.Changed = append(report.Changed, row)
			}
		}
	}
	if cfg.JSONOutput {
		return json.NewEncoder(out).Encode(report)
	}
	switch {
	case len(report.Changed) > 0:
		printBrowserTable(out, report.Changed)
	case target != nil:
		fmt.Fprintln(out, "Already assigned: that profile runs only in that browser.")
	default:
		fmt.Fprintln(out, "That profile is not bound to any browser; it uses the default browser.")
	}
	return nil
}

// bridgeBrowsers connects to the running bridge and lists its browsers.
func bridgeBrowsers() (*extension.RemoteSender, []extension.ConnInfo, error) {
	_, base, ok := findRunningBridge()
	if !ok {
		return nil, nil, errNotFound("%s", noBridgeHint)
	}
	sender := extension.NewRemoteSender(base)
	infos, err := sender.Browsers()
	if err != nil {
		return nil, nil, errAuthConnection("listing browsers: %v", err)
	}
	return sender, infos, nil
}

// attachedNames lists the browsers for an error message.
func attachedNames(infos []extension.ConnInfo) string {
	if len(infos) == 0 {
		return "none"
	}
	names := make([]string, len(infos))
	for i, b := range infos {
		names[i] = browserName(b)
	}
	return strings.Join(names, ", ")
}

// knownSet is the profiles a browser runs now, or an error when the bridge
// cannot say (it predates sets and the browser may run several).
func knownSet(b extension.ConnInfo) ([]string, error) {
	if b.Legacy {
		return nil, errInvalidInput("that browser's extension is too old to bind — reload it from the browser's extensions page (it needs version 1.5.0 or later)")
	}
	if !b.SetKnown() {
		return nil, errInvalidInput("the running bridge predates profile sharing and cannot say which profiles %s runs — restart it (the daemon or `monoagentcli extension serve`) and try again", browserName(b))
	}
	return b.ProfileSet(), nil
}

func addProfile(set []string, id string) []string {
	if slices.Contains(set, id) {
		return slices.Clone(set)
	}
	return append(slices.Clone(set), id)
}

func removeProfile(set []string, id string) []string {
	out := make([]string, 0, len(set))
	for _, p := range set {
		if p != id {
			out = append(out, p)
		}
	}
	return out
}

// setBrowserProfiles asks one browser's extension to run exactly these
// profiles. An extension before 1.8.0 keeps only the first id it is sent,
// so sharing is refused up front rather than silently dropping profiles.
func setBrowserProfiles(sender *extension.RemoteSender, b extension.ConnInfo, set []string) error {
	if b.Legacy {
		return errInvalidInput("that browser's extension is too old to bind — reload it from the browser's extensions page (it needs version 1.5.0 or later)")
	}
	if len(set) > 1 && !extension.SupportsProfileSets(b.Version) {
		return errInvalidInput("%s runs extension %q, which can run only one profile — reload the extension from the browser's extensions page (it needs 1.8.0 or later) to share it between profiles",
			browserName(b), b.Version)
	}
	if err := sender.SetBinding(b.Instance, set, ""); err != nil {
		return fmt.Errorf("binding %s: %w (if the extension is older than 1.5.0, reload it)", b.Instance, err)
	}
	return nil
}
