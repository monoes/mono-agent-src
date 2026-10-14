package summarycli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/capture"
	"github.com/monoes/mono-agent/internal/capturesummary"
	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/recording"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/summary"
)

// summaryOptions is what `summary` reads for one profile. It is shared by
// the profile view and by --all-profiles, which builds it once per profile.
func (d Deps) summaryOptions(db *storage.Database, profileID string, want map[string]bool) summary.Options {
	root := profiledir.Root(db.DB, profileID)
	opts := summary.Options{
		DB: db.DB, ProfileID: profileID, Now: time.Now(), Sections: want,
		Workflows:     readOnlyHybridStore(db),
		DaemonRunning: func() bool { _, live := daemonhb.Read(); return live },
		DaemonSchedules: func() map[string]time.Time {
			hb, live := daemonhb.Read()
			if !live {
				return nil
			}
			out := make(map[string]time.Time, len(hb.Schedules))
			for _, s := range hb.Schedules {
				if t, err := time.Parse(time.RFC3339, s.NextRun); err == nil {
					out[s.WorkflowID+"/"+s.NodeID] = t
				}
			}
			return out
		},
		Daemon: summaryDaemon,
		Bridge: d.summaryBridge,
		OrgServe: func() (bool, []string) {
			hb, live := monomind.ReadServeHeartbeat(root)
			if hb == nil || !live {
				return false, nil
			}
			return true, hb.Running
		},
		Recordings: func() ([]recording.Summary, error) {
			// profileID is already an id (initDB resolved cfg's; the
			// --all-profiles loop reads ids from the table).
			if err := recording.SetProfile(strings.TrimSpace(profileID)); err != nil {
				return nil, err
			}
			return recording.List()
		},
		Captures: func() ([]capture.Entry, error) {
			inbox, err := capture.ProfileInbox(profileID)
			if err != nil {
				return nil, err
			}
			return capture.List(inbox)
		},
		SummaryState: func(dir string) string { return capturesummary.StateOf(dir, time.Now()) },
	}
	if want == nil || want["automations"] {
		if reg, err := d.OpenAutomationRegistry(); err == nil {
			opts.Automations = summaryAutomations{reg: reg, db: db.DB}
		}
	}
	return opts
}

// sharedSections read the same whichever profile asks: the background
// services and the installed automation packages are per machine.
var sharedSections = map[string]bool{"services": true, "automations": true}

// splitSections divides the wanted sections (nil = all) into the ones built
// per profile and the ones built once.
func splitSections(want map[string]bool) (perProfile, shared map[string]bool) {
	perProfile, shared = map[string]bool{}, map[string]bool{}
	for _, n := range summary.SectionNames {
		if want != nil && !want[n] || n == "tasks" { // a board is one profile's: no all-profiles view (D9)
			continue
		}
		if sharedSections[n] {
			shared[n] = true
		} else {
			perProfile[n] = true
		}
	}
	return perProfile, shared
}

// buildGlobalSummary is `summary --all-profiles`: every profile's sections,
// merged, plus the shared ones once. The profile running the command is
// marked current.
func (d Deps) buildGlobalSummary(ctx context.Context, db *storage.Database, want map[string]bool) (summary.Summary, error) {
	profiles, err := profiledir.List(ctx, db.DB)
	if err != nil {
		return summary.Summary{}, fmt.Errorf("list profiles: %w", err)
	}
	perProfile, shared := splitSections(want)
	parts := make([]summary.ProfilePart, 0, len(profiles))
	if len(perProfile) > 0 {
		for _, p := range profiles {
			parts = append(parts, summary.ProfilePart{
				ID: p.ID, Name: p.Name,
				S: summary.Build(ctx, d.summaryOptions(db, p.ID, perProfile)),
			})
		}
	}
	sharedSum := summary.Summary{GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	if len(shared) > 0 {
		sharedSum = summary.Build(ctx, d.summaryOptions(db, d.ProfileID(), shared))
	}
	return summary.Merge(parts, sharedSum, d.ProfileID()), nil
}
