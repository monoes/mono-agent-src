// internal/matching/evaluate.go
package matching

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/monoes/mono-agent/internal/applications"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/monomind"
)

// ExecFunc, EnsureFunc, and SearchKnowledgeFunc are Evaluate's external
// dependencies, exposed as swappable package-level variables (the same
// pattern internal/documents.RenderPDFFunc established in Phase 4) so
// tests can inject fakes without a real agent runtime or monomind binary.
var (
	ExecFunc            = monomind.Exec
	EnsureFunc          = monomind.Ensure
	SearchKnowledgeFunc = monomind.SearchKnowledge
)

// evaluateTimeout bounds one evaluation's agent turn. Shorter than
// agent.ask's 300s default since this is a single reasoning response, not
// a coding task with file edits or tool use.
const evaluateTimeout = 120 * time.Second

// Evaluate scores applicationID (must be kind=job) against profileID's
// ingested knowledge. Persists the result as a new application_evaluations
// row and tags the application "fit:<verdict-slug>". See the design spec for
// the full rubric and rationale.
//
// runtime selects the backend: "jev" or "jev:<model>" asks the active Jev
// decision provider (TypeSafe by default), "jev@<provider>[:<model>]" a named
// one, the three gates and four rubric dimensions in one request and computes
// the verdict in Go (stored runtime "jev:<model>", or "jev@<provider>:<model>";
// a missing key is an error, never a silent fallback). Any other value is a local agent runtime
// delegated through ExecFunc.
func Evaluate(ctx context.Context, db *sql.DB, profileID, applicationID, runtime string) (*FitVerdict, error) {
	store := applications.NewStore(db)
	app, err := store.Get(ctx, profileID, applicationID)
	if err != nil {
		return nil, fmt.Errorf("matching.Evaluate: %w", err)
	}
	if app.Kind != applications.KindJob {
		return nil, fmt.Errorf("matching.Evaluate: only job-kind applications can be scored by this rubric, got kind %q", app.Kind)
	}

	// Resolve the Jev key before any other work so a missing key fails fast.
	jevProvider, jevModel, useJev := jevRuntime(runtime)
	var jc *jev.Client
	if useJev {
		if jc, err = newJevClient(ctx, db, profileID, jevProvider, jevModel); err != nil {
			return nil, fmt.Errorf("matching.Evaluate: %w", err)
		}
	}

	bin, _, err := EnsureFunc(ctx)
	if err != nil {
		return nil, fmt.Errorf("matching.Evaluate: %w", err)
	}

	query := app.Job.Title
	if len(app.Job.Description) > 0 {
		descLen := len(app.Job.Description)
		if descLen > 200 {
			descLen = 200
		}
		query += " " + app.Job.Description[:descLen]
	}
	excerpts, err := SearchKnowledgeFunc(ctx, db, profileID, query)
	if err != nil {
		return nil, fmt.Errorf("matching.Evaluate: searching profile knowledge: %w", err)
	}

	var verdict *FitVerdict
	storedRuntime := runtime
	if useJev {
		verdict, storedRuntime, err = evaluateJev(ctx, jc, app, excerpts)
		if err != nil {
			return nil, fmt.Errorf("matching.Evaluate: %w", err)
		}
	} else if verdict, err = evaluateAgent(ctx, bin, runtime, app, excerpts); err != nil {
		return nil, fmt.Errorf("matching.Evaluate: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = db.ExecContext(ctx, `
		INSERT INTO application_evaluations (id, application_id, runtime, eligibility_pass, language_pass,
		        technical_score, experience_score, behavioral_score, career_score, location_pass,
		        overall_score, verdict, rationale, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), applicationID, storedRuntime, verdict.EligibilityPass, verdict.LanguagePass,
		verdict.TechnicalScore, verdict.ExperienceScore, verdict.BehavioralScore, verdict.CareerScore, verdict.LocationPass,
		verdict.OverallScore, verdict.Verdict, verdict.Rationale, now,
	)
	if err != nil {
		return nil, fmt.Errorf("matching.Evaluate: persisting evaluation: %w", err)
	}

	if err := store.AddTag(ctx, profileID, applicationID, "fit:"+slugify(verdict.Verdict)); err != nil {
		return nil, fmt.Errorf("matching.Evaluate: tagging application: %w", err)
	}

	return verdict, nil
}

// evaluateAgent runs one local agent turn with the rubric prompt and parses
// its JSON verdict.
func evaluateAgent(ctx context.Context, bin, runtime string, app *applications.Application, excerpts []monomind.KnowledgeResult) (*FitVerdict, error) {
	res, err := ExecFunc(ctx, monomind.ExecOptions{
		Bin:     bin,
		Runtime: runtime,
		Prompt:  buildPrompt(app, excerpts),
		Timeout: evaluateTimeout,
		// The excerpts travel in the prompt; the agent needs no files.
		Sandbox:          monomind.TurnSandboxMode,
		WorkspacePurpose: monomind.WorkspaceMatching,
	}, func(ev monomind.Event) {})
	if err != nil {
		return nil, fmt.Errorf("agent exec: %w", err)
	}
	if res.Err != nil {
		return nil, fmt.Errorf("agent turn failed: %s", res.Err.Error())
	}
	v, err := parseVerdict(res.ResultText)
	if err != nil {
		return nil, err
	}
	v.SandboxStatus = "" // never the model's own word
	if monomind.SandboxMissing(res.SandboxStatus) {
		v.SandboxStatus = res.SandboxStatus
	}
	return v, nil
}
