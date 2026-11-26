package org

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/profiledir"
)

// Bounds of `org pending --all`, beside pendingParallel: the time one org may
// take, the time all orgs of all profiles may take together, and how long a
// read that is out of time may still take to hand back what it has (killing
// monomind does not reach a grandchild that holds its output open). Variables:
// tests shorten them.
var (
	pendingAllOrgTimeout = pendingTimeout
	pendingAllTimeout    = 45 * time.Second
	pendingAllGrace      = time.Second
)

// crossProfileFlag lists the commands requireRoot leaves alone only when this
// boolean flag is true: they then read every profile's folder, none being "the"
// root, and ask whether the caller is the user before opening the database.
var crossProfileFlag = map[string]string{"org pending": "all"}

// pendingJob is one org of one profile to read, in that profile's folder.
type pendingJob struct {
	profile   profiledir.Profile
	root, org string
}

// runPendingAll is `org pending --all`: what the orgs of every profile wait
// on. It only reads, and it is the user's alone, as `hil pending --all` is.
func (env *orgEnv) runPendingAll(cmd *cobra.Command) error {
	// The turn-aware gate first: a damaged turn record refuses as operator_only too.
	if why := clicore.OperatorRefusal(opgate.Refusal); why != "" {
		return clicore.OperatorOnly("org pending --all reads every profile's organizations, which only the user may: %s; run it yourself in your terminal or the app", why)
	}
	if err := clicore.RequireOperator(env.d.RequireOperator, "list what the organizations of every profile wait on"); err != nil {
		return err
	}
	if env.d.ProfileID() != "" {
		return env.d.InvalidInput("--all lists every profile: leave out --profile, or leave out --all")
	}
	if env.Project() != "" {
		return env.d.InvalidInput("--project names one profile's org root; drop it to use --all")
	}
	db, err := env.d.OpenDB()
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer db.Close()
	profiles, err := profiledir.List(cmd.Context(), db.DB)
	if err != nil {
		return fmt.Errorf("list profiles: %w", err)
	}
	var jobs []pendingJob
	errs := []pendingError{}
	for _, p := range profiles {
		// This profile's own folder; nothing is created in it.
		root := profiledir.Root(db.DB, p.ID)
		if root == "" {
			errs = append(errs, pendingError{ProfileID: p.ID, ProfileName: p.Name, Error: "this profile has no folder"})
			continue
		}
		names, err := orgdesign.ListOrgNames(root)
		if err != nil {
			errs = append(errs, pendingError{ProfileID: p.ID, ProfileName: p.Name, Error: err.Error()})
			continue
		}
		for _, name := range names {
			jobs = append(jobs, pendingJob{profile: p, root: root, org: name})
		}
	}
	items, failed := readPendingJobs(cmd.Context(), jobs)
	return printJSONValue(map[string]interface{}{"v": 1, "items": items, "errors": append(errs, failed...)})
}

// pendingRead is one org's answer.
type pendingRead struct {
	items []pendingItem
	errs  []pendingError
}

// readPendingJobs reads the jobs' orgs, pendingParallel at once, within
// pendingAllTimeout, and returns what they wait on in the jobs' order, each
// item and error with its profile.
func readPendingJobs(ctx context.Context, jobs []pendingJob) ([]pendingItem, []pendingError) {
	ctx, cancel := context.WithTimeout(ctx, pendingAllTimeout)
	defer cancel()
	reads := make([]pendingRead, len(jobs))
	slots := make(chan struct{}, pendingParallel)
	done := make(chan struct{}, len(jobs))
	started := 0
	for i := range jobs {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			reads[i].errs = []pendingError{{Org: jobs[i].org, Error: "not read: the listing ran out of time"}}
			continue
		}
		started++
		go func(i int) {
			defer func() { <-slots; done <- struct{}{} }()
			reads[i] = readPendingJob(ctx, jobs[i])
		}(i)
	}
	for range started {
		<-done
	}
	items, errs := []pendingItem{}, []pendingError{}
	for i, r := range reads {
		p := jobs[i].profile
		for _, it := range r.items {
			it.ProfileID, it.ProfileName = p.ID, p.Name
			items = append(items, it)
		}
		for _, e := range r.errs {
			e.ProfileID, e.ProfileName = p.ID, p.Name
			errs = append(errs, e)
		}
	}
	return items, errs
}

// readPendingJob reads one org, and stops waiting for it soon after its time
// is up.
func readPendingJob(ctx context.Context, job pendingJob) pendingRead {
	ctx, cancel := context.WithTimeout(ctx, pendingAllOrgTimeout)
	defer cancel()
	answer := make(chan pendingRead, 1)
	go func() {
		items, errs := orgPending(ctx, job.root, job.org)
		answer <- pendingRead{items, errs}
	}()
	select {
	case r := <-answer:
		return r
	case <-ctx.Done():
	}
	select {
	case r := <-answer:
		return r
	case <-time.After(pendingAllGrace):
		return pendingRead{errs: []pendingError{{Org: job.org, Error: "timed out"}}}
	}
}
