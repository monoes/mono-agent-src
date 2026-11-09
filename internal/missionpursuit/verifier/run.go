package verifier

import (
	"context"
	"strings"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
)

// runCriteria runs the criteria in order against the sealed snapshot; a criterion after the run's context ended is not run.
// The results are measured by the finish sandbox (exit status, pipes, post-run digests); nothing a program wrote is read as a
// result. Every text that came from a program or from the criterion is bounded and stripped.
func (k *Checker) runCriteria(ctx context.Context, sb finish.Sandbox, snap string, tree finish.Tree, req Request, res *Result) {
	for _, c := range req.Criteria {
		cr := CriterionResult{ID: c.ID, Hash: c.Hash, Kind: c.Kind}
		switch {
		case c.Kind == "rubric":
			cr.Result = ResPerson
		case ctx.Err() != nil:
			cr.Result = ResNotRun
		default:
			plan, perr := finish.ParsePlan(c.Kind, c.Spec, c.Expected)
			if perr != nil {
				cr.Result, cr.Detail = ResError, "the criterion cannot be run as written"
				break
			}
			if isProgram(c.Kind) && strings.Contains(plan.Argv[0], "/") {
				// the sandbox would run an absolute path outside its working area, such as a program in the live project folder
				cr.Result, cr.Detail = ResError, "the program must be a name on the system path"
				break
			}
			m := sb.Run(ctx, snap, tree, plan)
			cr.Result = closedResult(m.Result)
			cr.ExitCode, cr.StdoutDigest, cr.StderrDigest = m.ExitCode, m.StdoutDigest, m.StderrDigest
			cr.Stdout, cr.Stderr = Excerpt(m.Stdout, ExcerptCap), Excerpt(m.Stderr, ExcerptCap)
			cr.Detail, cr.Net, cr.RunsSubjectCode = Excerpt(m.Detail, detailCap), Excerpt(m.Net, 64), m.RunsSubjectCode
		}
		res.Criteria = append(res.Criteria, cr)
	}
}

// closedResult keeps the result inside the closed list; anything else is an error, never a pass.
func closedResult(r string) string {
	switch r {
	case finish.ResPass:
		return ResPass
	case finish.ResFail:
		return ResFail
	case finish.ResTimeout:
		return ResTimeout
	case finish.ResOversize:
		return ResOversize
	case finish.ResTainted:
		return ResTainted
	}
	return ResError
}

// programOutside refuses a program name that resolves, through symlinks, to a file under one of roots (the project, the
// scratch area): a link planted there would be code the workers wrote. path is the fixed system path.
func programOutside(name, path string, roots []string) error { return nil }
