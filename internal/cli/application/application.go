package application

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/applications"

	"github.com/spf13/cobra"
)

// NewCommand returns the `application` command group: tracks job and
// tender applications through a shared pending/applied/rejected/cancelled
// pipeline. See docs/mastermind/specs/2026-09-05-applications-foundation-design.md.
func NewCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "application",
		Short: "Track job and tender applications",
	}
	cmd.AddCommand(
		d.newAddCmd(),
		d.newListCmd(),
		d.newGetCmd(),
		d.newStatusCmd(),
		d.newTagCmd(),
		d.newDiscoverCmd(),
		d.newEvaluateCmd(),
		d.newEvaluatePendingCmd(),
		d.newApplyCmd(),
		d.newSendCmd(),
	)
	return cmd
}

func (d Deps) newAddCmd() *cobra.Command {
	var kind, title, company, url, location, description, currency, jobType, source, postedAt string
	var issuingOrg, submissionDeadline, requiredCerts, bidDocs, publishedAt string
	var compMin, compMax, estValue float64
	var isRemote bool
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a new job or tender application",
		Example: `  monoagentcli application add --kind job --company Acme --url https://acme.example/jobs/1
  monoagentcli application add --kind tender --issuing-org "Ministry" --url https://t.example/1 --submission-deadline 2026-12-01`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()
			store := applications.NewStore(db.DB)

			app := &applications.Application{ProfileID: d.ProfileID(), Kind: applications.Kind(kind)}
			switch app.Kind {
			case applications.KindJob:
				var minPtr, maxPtr *float64
				if cmd.Flags().Changed("compensation-min") {
					minPtr = &compMin
				}
				if cmd.Flags().Changed("compensation-max") {
					maxPtr = &compMax
				}
				var remotePtr *bool
				if cmd.Flags().Changed("remote") {
					remotePtr = &isRemote
				}
				app.Job = &applications.JobDetails{
					Title: title, Company: company, URL: url, Location: location, Description: description,
					CompensationMin: minPtr, CompensationMax: maxPtr, Currency: currency,
					JobType: jobType, IsRemote: remotePtr, Source: source, PostedAt: postedAt,
				}
			case applications.KindTender:
				var estPtr *float64
				if cmd.Flags().Changed("estimated-value") {
					estPtr = &estValue
				}
				app.Tender = &applications.TenderDetails{
					Title: title, IssuingOrg: issuingOrg, URL: url, Description: description,
					SubmissionDeadline: submissionDeadline, EstimatedValue: estPtr, Currency: currency,
					RequiredCertifications: requiredCerts, BidDocumentsRequired: bidDocs, Source: source, PublishedAt: publishedAt,
				}
			default:
				return d.InvalidInput("--kind must be \"job\" or \"tender\", got %q", kind)
			}

			if err := store.Create(cmd.Context(), app); err != nil {
				if errors.Is(err, applications.ErrInvalidInput) {
					return d.InvalidInput("%v", err)
				}
				return fmt.Errorf("adding application: %w", err)
			}
			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]string{"id": app.ID, "kind": string(app.Kind), "status": string(app.Status)})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Added %s application %s.\n", app.Kind, app.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "Application kind: job or tender (required)")
	cmd.Flags().StringVar(&title, "title", "", "Job title or tender reference/name (required)")
	cmd.Flags().StringVar(&company, "company", "", "Job kind: hiring company")
	cmd.Flags().StringVar(&url, "url", "", "Job posting or tender URL")
	cmd.Flags().StringVar(&location, "location", "", "Job kind: location")
	cmd.Flags().StringVar(&description, "description", "", "Free-text description")
	cmd.Flags().Float64Var(&compMin, "compensation-min", 0, "Job kind: minimum compensation")
	cmd.Flags().Float64Var(&compMax, "compensation-max", 0, "Job kind: maximum compensation")
	cmd.Flags().StringVar(&currency, "currency", "", "Currency code, e.g. USD")
	cmd.Flags().StringVar(&jobType, "job-type", "", "Job kind: e.g. full_time, contract")
	cmd.Flags().BoolVar(&isRemote, "remote", false, "Job kind: whether the role is remote")
	cmd.Flags().StringVar(&source, "source", "manual", "Where this application came from")
	cmd.Flags().StringVar(&postedAt, "posted-at", "", "Job kind: when the job was posted")
	cmd.Flags().StringVar(&issuingOrg, "issuing-org", "", "Tender kind: issuing organization")
	cmd.Flags().StringVar(&submissionDeadline, "submission-deadline", "", "Tender kind: submission deadline (required for tender)")
	cmd.Flags().Float64Var(&estValue, "estimated-value", 0, "Tender kind: estimated value")
	cmd.Flags().StringVar(&requiredCerts, "required-certifications", "", "Tender kind: comma-separated")
	cmd.Flags().StringVar(&bidDocs, "bid-documents-required", "", "Tender kind: comma-separated")
	cmd.Flags().StringVar(&publishedAt, "published-at", "", "Tender kind: when published")
	cmd.MarkFlagRequired("kind")
	cmd.MarkFlagRequired("title")
	cmd.MarkFlagRequired("url")
	return cmd
}

func (d Deps) newListCmd() *cobra.Command {
	var kind, status, tag string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List applications",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()
			store := applications.NewStore(db.DB)

			apps, err := store.List(cmd.Context(), d.ProfileID(), applications.ListFilter{
				Kind: applications.Kind(kind), Status: applications.Status(status), Tag: tag,
			})
			if err != nil {
				return fmt.Errorf("listing applications: %w", err)
			}
			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(apps)
			}
			if len(apps) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No applications found.")
				return nil
			}
			table := d.NewTable(cmd.OutOrStdout(), []string{"ID", "Kind", "Status", "Title", "Tags", "Updated"})
			for _, a := range apps {
				title := ""
				if a.Job != nil {
					title = a.Job.Title
				}
				if a.Tender != nil {
					title = a.Tender.Title
				}
				table.Append([]string{a.ID, string(a.Kind), string(a.Status), truncate(title, 40), truncate(joinTags(a.Tags), 30), a.UpdatedAt})
			}
			table.Render()
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "Filter by kind: job or tender")
	cmd.Flags().StringVar(&status, "status", "", "Filter by status")
	cmd.Flags().StringVar(&tag, "tag", "", "Filter by tag")
	return cmd
}

func joinTags(tags []string) string {
	out := ""
	for i, t := range tags {
		if i > 0 {
			out += ","
		}
		out += t
	}
	return out
}

func (d Deps) newGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "get <id>",
		Short:   "Show one application's full detail and status history",
		Args:    cobra.ExactArgs(1),
		Example: `  monoagentcli application get 1c2e...`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()
			store := applications.NewStore(db.DB)

			app, err := store.Get(cmd.Context(), d.ProfileID(), args[0])
			if err != nil {
				if errors.Is(err, applications.ErrNotFound) {
					return d.NotFound("application %q not found", args[0])
				}
				return fmt.Errorf("getting application: %w", err)
			}
			log, err := store.StatusLog(cmd.Context(), d.ProfileID(), args[0])
			if err != nil {
				return fmt.Errorf("getting status log: %w", err)
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]interface{}{"application": app, "status_log": log})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ID:     %s\nKind:   %s\nStatus: %s\nTags:   %s\n", app.ID, app.Kind, app.Status, joinTags(app.Tags))
			if app.Job != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Company: %s\nURL:     %s\n", app.Job.Company, app.Job.URL)
			}
			if app.Tender != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Issuing Org: %s\nURL:         %s\nDeadline:    %s\n", app.Tender.IssuingOrg, app.Tender.URL, app.Tender.SubmissionDeadline)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "History:")
			for _, e := range log {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s -> %s (%s)\n", e.CreatedAt, e.FromStatus, e.ToStatus, e.Actor)
			}
			return nil
		},
	}
}

func (d Deps) newStatusCmd() *cobra.Command {
	var note string
	cmd := &cobra.Command{
		Use:     "status <id> set <status>",
		Short:   "Transition an application's status",
		Args:    cobra.ExactArgs(3),
		Example: `  monoagentcli application status 1c2e... set cancelled`,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, verb, status := args[0], args[1], args[2]
			if verb != "set" {
				return d.InvalidInput("expected \"set\", got %q", verb)
			}
			// "applied" is deliberately NOT reachable through this generic
			// command, even though pending->applied is a valid transition
			// at the store level: `application send` (a human-invoked
			// command) is meant to be the only path that records an
			// application as applied. Letting this generic command reach
			// the same state would bypass that invariant with no document
			// generation, no browser review, and no actual submission.
			// Mirrors internal/nodes/applications/set_status.go's
			// SetStatusNode guard, but compares case-insensitively (unlike
			// that guard) since this is a human-typed CLI arg rather than a
			// workflow-authored config value.
			if strings.EqualFold(status, string(applications.StatusApplied)) {
				return d.InvalidInput("\"applied\" is not a valid target here -- use `application send`, which requires an explicit human action")
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()
			store := applications.NewStore(db.DB)

			if err := store.SetStatus(cmd.Context(), d.ProfileID(), id, applications.Status(status), applications.ActorUser, note); err != nil {
				if errors.Is(err, applications.ErrNotFound) {
					return d.NotFound("application %q not found", id)
				}
				if errors.Is(err, applications.ErrInvalidTransition) {
					return d.InvalidInput("%v", err)
				}
				return fmt.Errorf("setting status: %w", err)
			}
			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]string{"id": id, "status": status})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Set %s to %s.\n", id, status)
			return nil
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "Optional note recorded in the transition ledger")
	return cmd
}

func (d Deps) newTagCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "tag <id> <add|remove> <tag>",
		Short:   "Add or remove a tag from an application",
		Args:    cobra.ExactArgs(3),
		Example: `  monoagentcli application tag 1c2e... add urgent`,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, action, tag := args[0], args[1], args[2]
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()
			store := applications.NewStore(db.DB)

			var opErr error
			switch action {
			case "add":
				opErr = store.AddTag(cmd.Context(), d.ProfileID(), id, tag)
			case "remove":
				opErr = store.RemoveTag(cmd.Context(), d.ProfileID(), id, tag)
			default:
				return d.InvalidInput("expected \"add\" or \"remove\", got %q", action)
			}
			if opErr != nil {
				if errors.Is(opErr, applications.ErrNotFound) {
					return d.NotFound("application %q not found", id)
				}
				return fmt.Errorf("%s tag: %w", action, opErr)
			}
			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]string{"id": id, "tag": tag, "action": action})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%sed tag %q on %s.\n", action, tag, id)
			return nil
		},
	}
	return cmd
}
