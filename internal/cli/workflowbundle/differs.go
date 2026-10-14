package workflowbundle

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/monoes/mono-agent/internal/automation"
)

// A bundled package whose id and version are already installed but whose
// content differs is never installed silently: it is reported as "differs"
// with the install review's diff, and only `workflow import
// --replace-automations` replaces it — through the same review and
// confirmation as `automation install --replace`.

// sameContent reports whether two packages have the same files (the
// generated CHECKSUMS file aside).
func sameContent(a, b *automation.Package) (bool, error) {
	fa, err := a.Files()
	if err != nil {
		return false, err
	}
	fb, err := b.Files()
	if err != nil {
		return false, err
	}
	skip := func(list []string) []string {
		out := list[:0:0]
		for _, f := range list {
			if f != automation.ChecksumsFile {
				out = append(out, f)
			}
		}
		return out
	}
	fa, fb = skip(fa), skip(fb)
	if len(fa) != len(fb) {
		return false, nil
	}
	for i := range fa {
		if fa[i] != fb[i] {
			return false, nil
		}
		x, err := fs.ReadFile(a.FS, fa[i])
		if err != nil {
			return false, err
		}
		y, err := fs.ReadFile(b.FS, fb[i])
		if err != nil {
			return false, err
		}
		if !bytes.Equal(x, y) {
			return false, nil
		}
	}
	return true, nil
}

// bundleDiffers checks a bundled package against the installed one of the
// same id and version. It returns the install dry-run review (with its
// Changes) when the content differs, or nil when it is the same.
func bundleDiffers(reg *automation.Registry, id string, b BundledAutomation) (*automation.InstallResult, error) {
	path, cleanup, err := stageBundledAutomation(id, b)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	bundled, err := automation.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("open bundled package %s: %w", id, err)
	}
	installed, err := reg.Get(id)
	if err != nil {
		return nil, err
	}
	same, err := sameContent(installed, bundled)
	if err != nil || same {
		return nil, err
	}
	return reviewStagedBundle(reg, id, b, path)
}

// differsSummary is the one-line diff of a differs review.
func differsSummary(r *automation.InstallResult) string {
	var parts []string
	if c := r.Review.Changes; c != nil {
		add := func(label string, v []string) {
			if len(v) > 0 {
				parts = append(parts, label+" "+strings.Join(v, ", "))
			}
		}
		add("adds domains", c.AddedDomains)
		add("adds steps", c.AddedSteps)
		add("adds call_action targets", c.AddedCallActions)
		add("adds scripts", c.AddedScripts)
		add("changes scripts", c.ChangedScripts)
	}
	if len(parts) == 0 {
		return "same version, different content (no permission changes)"
	}
	return "same version, different content: " + strings.Join(parts, "; ")
}

// replaceBundledAutomation replaces an installed package with the bundled
// copy: pinned bytes, review line and diff printed, confirmation asked
// unless --yes, installed with Replace.
func (d Deps) replaceBundledAutomation(reg *automation.Registry, id string, b BundledAutomation, review *automation.InstallResult, o ImportOptions) error {
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	fmt.Fprintf(out, "%s\n  %s\n", bundleReviewLine(review), differsSummary(review))
	if !o.Yes {
		if !o.Interactive {
			return d.ErrConfirmationRequired
		}
		if !confirmYes(o.In, out, fmt.Sprintf("Replace the installed %s %s with the bundled copy?", id, review.Version)) {
			return errors.New("replace declined")
		}
	}
	path, cleanup, err := stageBundledAutomation(id, b)
	if err != nil {
		return err
	}
	defer cleanup()
	res, err := reg.Install(path, automation.InstallOptions{Source: automation.SourceImported,
		ExpectSHA256: strings.ToLower(b.SHA256), Replace: true})
	if err != nil {
		return err
	}
	if res.ID != id {
		return fmt.Errorf("bundled package under %q installed as %q", id, res.ID)
	}
	return nil
}
