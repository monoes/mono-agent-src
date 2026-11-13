package verifier

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
)

// runCriteria runs the criteria in order against the sealed snapshot; a criterion after the run's context ended is not run.
// The results are measured by the finish sandbox (exit status, pipes, post-run digests); nothing a program wrote is read as a
// result. Every text that came from a program or from the criterion is bounded and stripped.
func (k *Checker) runCriteria(ctx context.Context, sb finish.Sandbox, conf Confiner, snap string, tree finish.Tree, req Request, res *Result) {
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
			if isProgram(c.Kind) {
				if perr := programOutside(plan.Argv[0], finish.SystemPath, []string{req.Root, k.cfg.ScratchBase}); perr != nil {
					cr.Result, cr.Detail = ResError, perr.Error()
					break
				}
			}
			ir, _ := conf.(InfraReporter)
			if ir != nil {
				ir.TakeInfra()
			}
			m := sb.Run(ctx, snap, tree, plan)
			if ir != nil {
				if infra := ir.TakeInfra(); len(infra) > 0 && isProgram(c.Kind) {
					cr.Result, cr.Detail = ResError, Excerpt("the confinement failed, not the criterion: "+strings.Join(infra, "; "), detailCap)
					res.Criteria = append(res.Criteria, cr)
					continue
				}
			}
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
// scratch area): a link planted there would be code the workers wrote. path is the fixed system path. A link that stays
// outside the roots is fine (/bin/sh is often a link). This is not a confinement: sh -c and python3 run anything.
func programOutside(name, path string, roots []string) error {
	for _, d := range filepath.SplitList(path) {
		cand := filepath.Join(d, name)
		st, err := os.Stat(cand)
		if err != nil || !st.Mode().IsRegular() || st.Mode()&0o111 == 0 {
			continue
		}
		real, err := filepath.EvalSymlinks(cand)
		if err != nil {
			return fmt.Errorf("the program cannot be resolved")
		}
		for _, root := range roots {
			if rr, err := filepath.EvalSymlinks(root); err == nil {
				root = rr
			}
			if real == root || strings.HasPrefix(real, root+string(filepath.Separator)) {
				return fmt.Errorf("the program is a link into the project or the scratch area")
			}
		}
		return nil
	}
	return nil // not on the path: the sandbox says so
}
