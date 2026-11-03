package ops

import (
	"fmt"
	"slices"
	"strings"
)

// Describe renders the typed constraints in plain words for the entry dialog (note 13.4 section 2, T24): an explicit
// "*" is spelled out ("any destination"), never shown as a bare star. Operations the charter does not enable are
// absent, because they are not enabled.
func (c Constraints) Describe() []string {
	var out []string
	list := func(vals []string, anyWords string) string {
		if slices.Contains(vals, Any) {
			return anyWords
		}
		return strings.Join(vals, ", ")
	}
	if s := c.SendThirdParty; s != nil {
		out = append(out, fmt.Sprintf("send to third parties on %s as %s: to %s; at most %d per run; content class %s",
			s.Platform, s.Account, list(s.Destinations, "ANY DESTINATION"), s.MaxPerRun, s.ContentClass))
	}
	if v := c.VaultUse; v != nil {
		out = append(out, fmt.Sprintf("use the vault secrets %s with %s; at most %d requests; the agent learns only the status, a size bucket and one yes/no marker",
			strings.Join(v.Secrets, ", "), list(v.Hosts, "ANY HOST"), v.MaxAttempts))
	}
	if l := c.LocalDestructive; l != nil {
		out = append(out, fmt.Sprintf("%s files under %s; at most %d files and %d bytes per run",
			strings.Join(l.Verbs, " and "), strings.Join(l.Roots, ", "), l.MaxCount, l.MaxBytes))
	}
	if h := c.HostSetting; h != nil {
		keys := make([]string, 0, len(h.Keys))
		for k := range h.Keys {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			out = append(out, fmt.Sprintf("change the setting %s to %s", k, list(h.Keys[k], "ANY VALUE")))
		}
	}
	return out
}
