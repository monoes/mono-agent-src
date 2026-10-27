package org

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/orgenv"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgbridge"
	"github.com/monoes/mono-agent/internal/orgdecide"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/storage"
)

// orgSummaryRow is one org in `org summary`. NeedsYou is null in --fast
// mode and when it could not be computed (NeedsYouError says why).
type orgSummaryRow struct {
	Name          string `json:"name"`
	Running       bool   `json:"running"`
	Level         string `json:"level"`
	Paused        bool   `json:"paused"`
	Queued        int    `json:"queued"`
	NeedsYou      *int   `json:"needs_you"`
	NeedsYouError string `json:"needs_you_error,omitempty"`
	// Set only with --all-profiles.
	ProfileID   string `json:"profile_id,omitempty"`
	ProfileName string `json:"profile_name,omitempty"`
}

// perOrgNeedsYouTimeout bounds the monomind round-trips for one org.
var perOrgNeedsYouTimeout = 10 * time.Second // var: tests shorten it

// summaryNeedsYou lets tests hold a worker past its deadline without a real
// subprocess or pipe, so the outer timeout can be checked with virtual time.
var summaryNeedsYou = (*orgEnv).needsYou

func newOrgSummaryCmd(env *orgEnv) *cobra.Command {
	var fast, allProfiles bool
	c := &cobra.Command{
		Use:   "summary",
		Short: "One row per org: running, autonomy level, queued messages, items waiting for you",
		Long: "Cross-org roll-up for the dashboard. --fast reads local files and the database only " +
			"(no monomind process) and leaves needs_you null. Without it, needs_you is computed " +
			"for every org in parallel, each bounded to 10 seconds.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if allProfiles {
				if env.Project() != "" {
					return env.d.InvalidInput("--project names one profile's org root; drop it to use --all-profiles")
				}
				// Names every profile, so it needs no --profile: open the
				// database without resolving one.
				db, err := env.d.OpenDB()
				if err != nil {
					return err
				}
				defer db.Close()
				profiles, err := profiledir.List(cmd.Context(), db.DB)
				if err != nil {
					return fmt.Errorf("list profiles: %w", err)
				}
				rows := []orgSummaryRow{}
				serve := false
				for _, p := range profiles {
					r, live, err := env.orgSummaryFor(cmd.Context(), profiledir.Root(db.DB, p.ID), db, p.ID, nil, fast)
					if err != nil {
						return fmt.Errorf("orgs of %s: %w", p.Name, err)
					}
					for i := range r {
						r[i].ProfileID, r[i].ProfileName = p.ID, p.Name
					}
					rows = append(rows, r...)
					serve = serve || live
				}
				return printOrgSummary(rows, fast, serve, "global")
			}
			root, err := env.RootErr()
			if err != nil {
				return err
			}
			db, profileID, _, dbErr := env.Profile()
			rows, live, err := env.orgSummaryFor(cmd.Context(), root, db, profileID, dbErr, fast)
			if err != nil {
				return err
			}
			return printOrgSummary(rows, fast, live, "profile")
		},
	}
	c.Flags().BoolVar(&fast, "fast", false, "Local files and database only: skip needs_you (no monomind process)")
	c.Flags().BoolVar(&allProfiles, "all-profiles", false, "Every profile's orgs, each row tagged with its profile")
	return c
}

// orgSummaryFor is one profile's org rows. db may be nil (dbErr says why):
// the local facts (running, queued) are still reported.
func (env *orgEnv) orgSummaryFor(ctx context.Context, root string, db *storage.Database, profileID string, dbErr error, fast bool) ([]orgSummaryRow, bool, error) {
	names, err := orgdesign.ListOrgNames(root)
	if err != nil {
		return nil, false, err
	}
	hb, serveLive := monomind.ReadServeHeartbeat(root)
	running := map[string]bool{}
	if hb != nil && serveLive {
		for _, n := range hb.Running {
			running[n] = true
		}
	}
	var svc *orgdecide.Service
	if dbErr == nil {
		svc = orgdecide.NewService(db.DB, nil)
		wfs := orgenv.GrantWorkflows(db, env.OpenWorkflowStore)
		svc.LoadWorkflow, svc.ListWorkflows = wfs.GetWorkflow, wfs.ListWorkflows
	}
	now := time.Now()
	rows := make([]orgSummaryRow, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		row := orgSummaryRow{Name: name, Running: running[name] || monomind.OrgRunLive(root, name), Level: orgdesign.LevelManual}
		if svc != nil {
			if a, err := svc.Store.Get(ctx, profileID, name); err == nil && a != nil {
				row.Level = a.EffectiveLevel(now)
				row.Paused = a.PausedUntil != nil && a.PausedUntil.After(now)
			}
		}
		if msgs, _, err := orgbridge.ReadQueued(root, name); err == nil {
			row.Queued = len(msgs)
		}
		rows[i] = row
		if fast {
			continue
		}
		if dbErr != nil {
			rows[i].NeedsYouError = dbErr.Error()
			continue
		}
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(ctx, perOrgNeedsYouTimeout)
			defer cancel()
			// Killing monomind on timeout only reaches the direct child: a
			// wrapper's grandchild can hold the pipe open past the deadline.
			// Stop waiting at the deadline instead of trusting the kill.
			type result struct {
				n   int
				err error
			}
			done := make(chan result, 1)
			go func() {
				items, err := summaryNeedsYou(env, ctx, db, profileID, root, name)
				done <- result{len(items), err}
			}()
			select {
			case r := <-done:
				if r.err != nil {
					rows[i].NeedsYouError = r.err.Error()
					return
				}
				n := r.n
				rows[i].NeedsYou = &n
			case <-ctx.Done():
				rows[i].NeedsYouError = "timed out after " + perOrgNeedsYouTimeout.String()
			}
		}(i, name)
	}
	wg.Wait()
	return rows, serveLive, nil
}

// printOrgSummary writes the rows with their totals.
func printOrgSummary(rows []orgSummaryRow, fast, serveLive bool, scope string) error {
	totals := map[string]int{"orgs": len(rows), "running": 0, "queued": 0, "needs_you": 0}
	for _, r := range rows {
		if r.Running {
			totals["running"]++
		}
		totals["queued"] += r.Queued
		if r.NeedsYou != nil {
			totals["needs_you"] += *r.NeedsYou
		}
	}
	return printJSONValue(map[string]interface{}{
		"v": 1, "fast": fast, "serve_running": serveLive, "scope": scope, "orgs": rows, "totals": totals,
	})
}
