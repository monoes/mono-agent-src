package workflowbundle

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/automation"
)

// Import side of workflow bundles (see workflow_bundle.go for the format):
// report bundled packages as present / missing / differs, install missing
// ones after a pinned review, and never replace an installed one silently.

// ImportItem reports what `workflow import` did (or would do) with
// one bundled automation.
type ImportItem struct {
	ID               string `json:"id"`
	Version          string `json:"version"`
	Status           string `json:"status"` // present | differs | replaced | conflict | missing | installed | failed
	InstalledVersion string `json:"installedVersion,omitempty"`
	Error            string `json:"error,omitempty"`
	// NotBundled: the exporter could not include this package (see Error);
	// --yes cannot install it.
	NotBundled bool `json:"notBundled,omitempty"`
	// LocalOnly: a notBundled package that opens local addresses; only
	// recreating it on this machine helps.
	LocalOnly bool `json:"localOnly,omitempty"`
	// Hint is what to do next, as plain text (Error keeps the combined
	// message for older readers).
	Hint string `json:"hint,omitempty"`
	// Builtin and Replaceable qualify a "differs" item: a built-in is never
	// replaced from a bundle (replaceable false).
	Builtin     bool  `json:"builtin,omitempty"`
	Replaceable *bool `json:"replaceable,omitempty"`
	// Review and ReviewDetail describe a "missing" or "differs" package (a
	// dry-run review of the pinned bytes, nothing installed), so a GUI can
	// show what it would install before asking. Changes is the diff against
	// the installed copy for "differs".
	Changes      *automation.ReviewChanges `json:"changes,omitempty"`
	Review       string                    `json:"review,omitempty"`
	ReviewDetail *ReviewDetail             `json:"reviewDetail,omitempty"`
}

// ReviewDetail is the structured form of bundleReviewLine.
type ReviewDetail struct {
	ID           string               `json:"id"`
	Version      string               `json:"version"`
	Publisher    string               `json:"publisher,omitempty"`
	Domains      []string             `json:"domains"`
	Capabilities []string             `json:"capabilities"`
	Replaces     *automation.Replaced `json:"replaces,omitempty"`
	// The install review's confirmation fields, as `automation install
	// --dry-run` reports them, so the import dialog can show the same.
	ReplaceRequired bool                    `json:"replaceRequired"`
	TrustChange     *automation.TrustChange `json:"trustChange,omitempty"`
	Visibility      map[string][]string     `json:"visibility"`
}

// ImportOptions controls handleBundledAutomations.
type ImportOptions struct {
	Yes         bool // install missing packages without asking
	Replace     bool // --replace-automations: replace installed packages whose bundled copy differs
	Interactive bool // a person can answer a prompt on In
	In          io.Reader
	Out         io.Writer // prompts and reviews
}

// HandleBundledAutomations reads the "automations" field of raw workflow
// JSON. Bundled packages that are already installed are "present"; the
// others are installed with --yes (or after a prompt), and otherwise
// reported as "missing". Returns nil when the file bundles nothing.
// Failures are reported per package and never fail the workflow import.
func (d Deps) HandleBundledAutomations(raw []byte, o ImportOptions) []ImportItem {
	var doc struct {
		Automations map[string]BundledAutomation   `json:"automations"`
		Unbundled   map[string]UnbundledAutomation `json:"unbundledAutomations"`
	}
	if json.Unmarshal(raw, &doc) != nil || len(doc.Automations)+len(doc.Unbundled) == 0 {
		return nil
	}
	ids := make([]string, 0, len(doc.Automations))
	for id := range doc.Automations {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]ImportItem, 0, len(ids))
	reg, err := d.OpenRegistry()
	if err != nil {
		for _, id := range ids {
			items = append(items, ImportItem{ID: id, Version: doc.Automations[id].Version, Status: "failed", Error: err.Error()})
		}
		return items
	}
	// Every id in the index, removed built-ins included (Info hides those).
	all, err := reg.List(true)
	if err != nil {
		for _, id := range ids {
			items = append(items, ImportItem{ID: id, Version: doc.Automations[id].Version, Status: "failed", Error: err.Error()})
		}
		return items
	}
	known := make(map[string]automation.InstalledInfo, len(all))
	for _, in := range all {
		known[in.ID] = in
	}
	for _, id := range ids {
		b := doc.Automations[id]
		item := ImportItem{ID: id, Version: b.Version, Status: "missing"}
		// Never install over an installed id, including an uninstalled
		// built-in (its index entry stays, marked removed).
		if info, ok := known[id]; ok {
			item.Status, item.InstalledVersion = "present", info.Version
			if info.Removed {
				item.Status, item.Error = "conflict", "a removed built-in has this id; reinstall it from monoes.me (`monoagentcli library login`, then `monoagentcli library install automation "+id+"`)"
			} else if info.Version == b.Version {
				d.checkDiffers(reg, id, b, o, &item)
			}
			items = append(items, item)
			continue
		}
		if o.Yes || o.Interactive {
			if err := installBundledAutomation(reg, id, b, o); err != nil {
				item.Status, item.Error = "failed", err.Error()
			} else {
				item.Status = "installed"
			}
		} else if review, err := reviewBundledAutomation(reg, id, b); err != nil {
			item.Error = err.Error() // still "missing": --yes would fail the same way
		} else {
			item.Review, item.ReviewDetail = bundleReviewLine(review), NewReviewDetail(review)
		}
		items = append(items, item)
	}
	return append(items, unbundledItems(doc.Unbundled, doc.Automations, known)...)
}

// unbundledItems reports packages the exporter could not bundle: present
// when installed here, otherwise missing with the exporter's reason and hint.
func unbundledItems(unbundled map[string]UnbundledAutomation, bundled map[string]BundledAutomation,
	known map[string]automation.InstalledInfo) []ImportItem {
	ids := make([]string, 0, len(unbundled))
	for id := range unbundled {
		if _, dup := bundled[id]; !dup {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var items []ImportItem
	for _, id := range ids {
		u := unbundled[id]
		item := ImportItem{ID: id, Version: u.Version, Status: "missing", NotBundled: true,
			LocalOnly: u.LocalOnly, Error: "not in the bundle: " + u.Reason,
			Hint: fmt.Sprintf("Recreate %s on this machine, or ask the sender for it.", id)}
		if u.Hint != "" {
			item.Error += "; the sender can " + u.Hint
			item.Hint = "Ask the sender to " + u.Hint + "."
		}
		if info, ok := known[id]; ok && !info.Removed {
			item.Status, item.InstalledVersion, item.Error, item.Hint = "present", info.Version, "", ""
		}
		items = append(items, item)
	}
	return items
}

// installBundledAutomation installs one bundled package that is not
// installed yet. The bundle is untrusted input: the sha256 is required and
// pinned through to the install, the package's manifest id must equal its
// bundle key, and a one-line review summary is always printed (also with
// --yes) before anything is written.
func installBundledAutomation(reg *automation.Registry, id string, b BundledAutomation, o ImportOptions) error {
	path, cleanup, err := stageBundledAutomation(id, b)
	if err != nil {
		return err
	}
	defer cleanup()
	review, err := reviewStagedBundle(reg, id, b, path)
	if err != nil {
		return err
	}
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	fmt.Fprintln(out, bundleReviewLine(review))
	if !o.Yes && !confirmYes(o.In, out, fmt.Sprintf("Install %s %s?", review.ID, review.Version)) {
		return errors.New("install declined")
	}
	res, err := reg.Install(path, automation.InstallOptions{Source: automation.SourceImported, ExpectSHA256: strings.ToLower(b.SHA256)})
	if err != nil {
		return err
	}
	if res.ID != id {
		return fmt.Errorf("bundled package under %q installed as %q", id, res.ID)
	}
	return nil
}

// reviewBundledAutomation is the dry-run half of installBundledAutomation:
// the same sha256 pinning and id checks, and nothing is installed.
func reviewBundledAutomation(reg *automation.Registry, id string, b BundledAutomation) (*automation.InstallResult, error) {
	path, cleanup, err := stageBundledAutomation(id, b)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return reviewStagedBundle(reg, id, b, path)
}

// stageBundledAutomation decodes one bundled package, checks it against its
// pinned sha256 and writes it to a private temp file. The caller runs
// cleanup.
func stageBundledAutomation(id string, b BundledAutomation) (string, func(), error) {
	data, err := base64.StdEncoding.DecodeString(b.Mpkg)
	if err != nil {
		return "", nil, fmt.Errorf("decode bundled package: %w", err)
	}
	want := strings.ToLower(b.SHA256)
	if want == "" {
		return "", nil, fmt.Errorf("bundled package %s has no sha256", id)
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != want {
		return "", nil, fmt.Errorf("bundled package %s: sha256 mismatch", id)
	}
	dir, err := os.MkdirTemp("", "workflow-bundle-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	path := filepath.Join(dir, "bundle.mpkg")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

// reviewStagedBundle opens a staged bundle, checks its manifest id and runs
// the install dry run (pinned to the bundle's sha256).
func reviewStagedBundle(reg *automation.Registry, id string, b BundledAutomation, path string) (*automation.InstallResult, error) {
	pkg, err := automation.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("open bundled package %s: %w", id, err)
	}
	if pkg.Manifest.ID != id {
		return nil, fmt.Errorf("bundled package under %q declares id %q; not installed", id, pkg.Manifest.ID)
	}
	review, err := reg.Install(path, automation.InstallOptions{Source: automation.SourceImported, ExpectSHA256: strings.ToLower(b.SHA256), DryRun: true})
	if err != nil {
		return nil, err
	}
	if review.ID != id {
		return nil, fmt.Errorf("bundled package under %q reviews as %q; not installed", id, review.ID)
	}
	if issuesHaveErrors(review.Issues) {
		return nil, fmt.Errorf("bundled package %s has validation errors; not installed", id)
	}
	return review, nil
}

// NewReviewDetail is bundleReviewLine as data.
func NewReviewDetail(r *automation.InstallResult) *ReviewDetail {
	rv := r.Review
	// The registry's plain-language capabilities, then the short technical
	// list the review line prints (steps, scripts, downloads, tier).
	caps := append(append([]string{}, rv.Capabilities...), bundleCapabilities(rv)...)
	return &ReviewDetail{
		ID: r.ID, Version: r.Version, Publisher: rv.Publisher,
		Domains: append([]string{}, rv.Domains...), Capabilities: caps,
		Replaces:        rv.Replaces,
		ReplaceRequired: rv.ReplaceRequired,
		TrustChange:     rv.TrustChange,
		Visibility:      nonNilVisibility(rv.Visibility),
	}
}

func nonNilVisibility(v map[string][]string) map[string][]string {
	if v == nil {
		return map[string][]string{}
	}
	return v
}

// bundleReviewLine summarises an install review on one line: id, version,
// publisher, domains and capabilities.
func bundleReviewLine(r *automation.InstallResult) string {
	rv := r.Review
	return fmt.Sprintf("Bundled automation %s %s — publisher %s — domains %s — %s",
		r.ID, r.Version, orDash(rv.Publisher), orDash(strings.Join(rv.Domains, ",")), strings.Join(bundleCapabilities(rv), "; "))
}

// bundleCapabilities is the short capability list of the review line.
func bundleCapabilities(rv automation.Review) []string {
	caps := []string{"steps: " + orDash(strings.Join(rv.Steps, ","))}
	if len(rv.Steps) == 0 {
		caps[0] = "steps: unrestricted"
	}
	if len(rv.Scripts) > 0 {
		caps = append(caps, "scripts: "+strings.Join(rv.Scripts, ","))
	}
	if rv.Downloads {
		caps = append(caps, "downloads")
	}
	if rv.Tier != "" {
		caps = append(caps, "tier: "+rv.Tier)
	}
	if rv.PolicyBlocked {
		caps = append(caps, "policy-blocked")
	}
	return caps
}

// PrintBundleImport prints the human summary of handleBundledAutomations.
func PrintBundleImport(out io.Writer, items []ImportItem) {
	missing := 0
	for _, it := range items {
		if it.NotBundled && it.Status == "missing" {
			// --yes cannot help: the package is not in the file.
			fmt.Fprintf(out, "Automation %s %s: missing (%s)\n  To run this workflow here, recreate %s on this machine or ask the sender for it.\n",
				it.ID, it.Version, it.Error, it.ID)
			continue
		}
		switch it.Status {
		case "differs":
			fmt.Fprintf(out, "Automation %s %s: differs from the installed copy — %s\n", it.ID, it.Version, it.Error)
		case "replaced":
			fmt.Fprintf(out, "Automation %s %s: replaced with the bundled copy\n", it.ID, it.Version)
		case "present":
			fmt.Fprintf(out, "Automation %s: already installed (%s; bundle has %s)\n", it.ID, it.InstalledVersion, it.Version)
		case "installed":
			fmt.Fprintf(out, "Automation %s %s: installed from the bundle\n", it.ID, it.Version)
		case "failed", "conflict":
			fmt.Fprintf(out, "Automation %s %s: not installed: %s\n", it.ID, it.Version, it.Error)
		default:
			missing++
			fmt.Fprintf(out, "Automation %s %s: missing (bundled)\n", it.ID, it.Version)
		}
	}
	if missing > 0 {
		fmt.Fprintln(out, "Pass --yes to `workflow import` to install bundled automations.")
	}
}

// checkDiffers turns a "present" item into "differs" when the bundled copy
// of the same version has other content, and replaces it only with
// --replace-automations (after the review, and confirmation unless --yes).
func (d Deps) checkDiffers(reg *automation.Registry, id string, b BundledAutomation, o ImportOptions, item *ImportItem) {
	review, err := bundleDiffers(reg, id, b)
	if err != nil {
		item.Error = "could not compare with the bundled copy: " + err.Error()
		return
	}
	if review == nil {
		return // same content
	}
	item.Status = "differs"
	item.Review, item.ReviewDetail = bundleReviewLine(review), NewReviewDetail(review)
	item.Changes = review.Review.Changes
	item.Hint = "Re-import with --replace-automations to review and replace it, or install the bundled copy with `automation install <file> --replace`."
	item.Error = differsSummary(review) + "; the installed copy was kept. To replace it, re-import with --replace-automations (shows this review and asks), or install the bundled copy with `automation install <file> --replace`"
	replaceable := true
	if info, err := reg.Info(id); err == nil && info.Trust == automation.TrustBuiltin {
		// A bundle never replaces a built-in or an official monoes.me
		// package (`library update` and rollback manage those).
		replaceable = false
		item.Builtin, item.Replaceable = true, &replaceable
		item.Hint = "A bundle never replaces a built-in; the installed copy stays."
		item.Error = differsSummary(review) + "; the installed copy is a built-in and a bundle never replaces it"
		return
	}
	item.Replaceable = &replaceable
	if !o.Replace {
		return
	}
	if err := d.replaceBundledAutomation(reg, id, b, review, o); err != nil {
		item.Error = "not replaced: " + err.Error()
		if errors.Is(err, d.ErrConfirmationRequired) {
			item.Hint = "Add --yes (or run in a terminal to confirm) together with --replace-automations."
		}
		return
	}
	item.Status, item.Error, item.Hint = "replaced", "", ""
}
