package doctor

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/health"
)

// addAutomationHooks feeds the doctor's automations group. It opens the
// registry without booting it (openAutomationRegistry may fold legacy
// actions and write files; a check must not), and reads selector health the way
// `automation doctor` does: a row whose key the package no longer
// declares is stale, not a problem.
func addAutomationHooks(env *health.Env) {
	env.Automations = func(ctx context.Context) (*health.AutomationsInfo, error) {
		// Opening the registry creates its folder; with nothing installed
		// there is nothing to report, and nothing may be created.
		if _, err := os.Stat(filepath.Join(env.DataDir, "automations")); err != nil {
			return &health.AutomationsInfo{}, nil
		}
		reg, err := automation.Default()
		if err != nil {
			return nil, err
		}
		infos, err := reg.List(false)
		if err != nil {
			return nil, err
		}
		out := &health.AutomationsInfo{Installed: len(infos)}
		installed := map[string]bool{}
		for _, i := range infos {
			installed[i.ID] = true
			if !i.Available {
				out.Unavailable = append(out.Unavailable, health.UnavailableAutomation{ID: i.ID, Reason: i.UnavailableReason})
			}
		}
		if env.DB == nil || !tableExists(ctx, env.DB, "automation_selector_health") {
			return out, nil
		}
		rows, err := automation.LoadSelectorHealth(env.DB, "")
		if err != nil {
			return nil, err
		}
		keysOf := automation.RegistrySelectorKeys(reg)
		declared := map[string]map[string]bool{}
		for _, h := range rows {
			if !installed[h.AutomationID] {
				continue
			}
			keys, seen := declared[h.AutomationID]
			if !seen {
				keys, _ = keysOf(h.AutomationID)
				declared[h.AutomationID] = keys
			}
			if keys != nil && !keys[h.Key] {
				continue // stale: the installed version no longer has this selector
			}
			status := h.Status
			if status == "" {
				status = automation.SelectorStatus(h)
			}
			if status == automation.HealthBroken || status == automation.HealthDecaying {
				out.Selectors = append(out.Selectors, health.SelectorProblem{AutomationID: h.AutomationID, Key: h.Key, Status: status})
			}
		}
		sort.Slice(out.Selectors, func(i, j int) bool {
			a, b := out.Selectors[i], out.Selectors[j]
			if a.Status != b.Status {
				return a.Status == automation.HealthBroken // broken first
			}
			if a.AutomationID != b.AutomationID {
				return a.AutomationID < b.AutomationID
			}
			return a.Key < b.Key
		})
		return out, nil
	}
}
