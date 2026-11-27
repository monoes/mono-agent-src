package release

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// install.sh and `monoagentcli update` read the releases repository's "latest" release and accept
// only a signed one. So a release is created not latest, and only scripts/release-latest.sh marks
// it, as the last step of the job that finished publishing it. These checks pin that.

type latestStep struct {
	Name            string            `yaml:"name"`
	ID              string            `yaml:"id"`
	Uses            string            `yaml:"uses"`
	Run             string            `yaml:"run"`
	If              string            `yaml:"if"`
	Shell           string            `yaml:"shell"`
	Env             map[string]string `yaml:"env"`
	With            map[string]any    `yaml:"with"`
	ContinueOnError any               `yaml:"continue-on-error"`
}

type latestJob struct {
	If              string         `yaml:"if"`
	Needs           any            `yaml:"needs"`
	Env             map[string]any `yaml:"env"`
	ContinueOnError any            `yaml:"continue-on-error"`
	Steps           []latestStep   `yaml:"steps"`
}

const (
	latestMarkRun       = "bash scripts/release-latest.sh mark"
	latestUnsignedIf    = "env.TOKEN_SET != 'true'"
	latestTokenSet      = "${{ secrets.RELEASES_REPO_TOKEN != '' }}"
	latestCreateInput   = "${{ steps.holds.outputs.holds == 'true' }}"
	latestHoldsLine     = `holds="$(bash scripts/release-latest.sh holds)"`
	latestHoldsOutput   = `echo "holds=${holds}" >> "$GITHUB_OUTPUT"`
	latestVerifyRun     = "bash scripts/release-latest.sh verify-published upload"
	latestSameLine      = `same="$(bash scripts/release-latest.sh same-manifest upload)"`
	latestSupersededRun = `superseded="$(bash scripts/release-latest.sh superseded)"`
)

// joinedLines returns the commands of a run block with backslash continuations joined.
func joinedLines(run string) []string {
	return strings.Split(strings.ReplaceAll(run, "\\\n", " "), "\n")
}

// latestViolations returns every broken rule about the "latest" release in the workflow.
func latestViolations(raw []byte) []string {
	var out []string
	bad := func(format string, a ...any) { out = append(out, fmt.Sprintf(format, a...)) }
	var wf struct {
		Jobs map[string]latestJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		return []string{"workflow does not parse: " + err.Error()}
	}

	creates, marks := 0, 0
	for jobName, job := range wf.Jobs {
		for i, st := range job.Steps {
			where := fmt.Sprintf("job %s step %d (%s)", jobName, i+1, st.Name)
			if strings.Contains(st.Uses, "action-gh-release") {
				creates++
				if jobName != "release" || fmt.Sprint(st.With["make_latest"]) != latestCreateInput {
					bad("%s: the release must be created with make_latest: %s, got %v", where, latestCreateInput, st.With["make_latest"])
				}
				if st.With["draft"] != nil || st.With["prerelease"] != nil {
					bad("%s: a draft or prerelease cannot be marked latest", where)
				}
				if i == 0 || job.Steps[i-1].ID != "holds" || !hasLine(job.Steps[i-1].Run, latestHoldsLine) ||
					!hasLine(job.Steps[i-1].Run, latestHoldsOutput) || !hasLine(job.Steps[i-1].Run, "set -euo pipefail") ||
					job.Steps[i-1].If != "" || job.Steps[i-1].ContinueOnError != nil {
					bad("%s: the step before it (id holds) must decide make_latest with release-latest.sh holds", where)
				}
			}
			for _, line := range joinedLines(st.Run) {
				if strings.Contains(line, "gh release create") && !strings.Contains(line, "--latest=false") {
					bad("%s: gh release create without --latest=false", where)
				}
				if strings.Contains(line, "gh release edit") || strings.Contains(line, "make_latest") {
					bad("%s: only scripts/release-latest.sh marks a release latest", where)
				}
			}
			if strings.Contains(st.Run, "release-latest.sh mark") {
				marks++
				if strings.TrimSpace(st.Run) != latestMarkRun || st.ContinueOnError != nil || st.Shell != "" {
					bad("%s: the marking step must run exactly %q and fail the job", where, latestMarkRun)
				}
				if i != len(job.Steps)-1 {
					bad("%s: marking latest must be the last step of its job", where)
				}
				switch jobName {
				case "release":
					if st.If != latestUnsignedIf || st.Env["GH_TOKEN"] != "${{ github.token }}" {
						bad("%s: the release job may mark latest only when no releases token is set (if %q)", where, st.If)
					}
				case "publish-releases-repo":
					if st.If != sshsigGate || st.Env["GH_TOKEN"] != "${{ secrets.RELEASES_REPO_TOKEN }}" {
						bad("%s: must mark latest with the releases token whenever the job publishes (if %q)", where, st.If)
					}
				default:
					bad("%s: job %s may not mark a release latest", where, jobName)
				}
			}
		}
	}
	if creates != 1 {
		bad("%d steps use action-gh-release, want exactly 1", creates)
	}
	if marks != 2 {
		bad("%d steps mark a release latest, want the release job's (no signing) and the publish job's", marks)
	}
	for _, name := range []string{"release", "sign-manifest", "publish-releases-repo"} {
		job := wf.Jobs[name]
		if job.ContinueOnError != nil {
			bad("job %s: continue-on-error", name)
		}
		for i, st := range job.Steps {
			if st.ContinueOnError != nil && st.Name != "Check the release environment has required reviewers" {
				bad("job %s step %d (%s): continue-on-error", name, i+1, st.Name)
			}
		}
	}
	if got := wf.Jobs["release"].Env["TOKEN_SET"]; got != latestTokenSet {
		bad("job release: env TOKEN_SET must be %s, got %v", latestTokenSet, got)
	}

	pub := wf.Jobs["publish-releases-repo"]
	if pub.Env["TOKEN_SET"] != latestTokenSet || pub.If != "" {
		bad("job publish-releases-repo: TOKEN_SET must match the release job's and the job may have no if (got %q)", pub.If)
	}
	needs, _ := pub.Needs.([]any)
	if !slices.Contains(needs, any("release")) || !slices.Contains(needs, any("sign-manifest")) {
		bad("job publish-releases-repo must need release and sign-manifest, got %v", pub.Needs)
	}
	index := func(needle string) int {
		found := -1
		n := 0
		for i, st := range pub.Steps {
			if strings.Contains(st.Run, needle) {
				found, n = i, n+1
			}
		}
		if n != 1 {
			bad("publish-releases-repo: %d steps contain %q, want exactly 1", n, needle)
			return -1
		}
		return found
	}
	upload, verify := index("gh release upload"), index("release-latest.sh verify-published")
	install, mark := index("contents/install.sh"), index("release-latest.sh mark")
	if upload < 0 || !(upload < verify && verify < install && install < mark) {
		bad("publish-releases-repo: want upload, verify, install.sh, mark latest (steps %d, %d, %d, %d)", upload, verify, install, mark)
		return out
	}
	if st := pub.Steps[verify]; strings.TrimSpace(st.Run) != latestVerifyRun || st.If != sshsigGate || st.Shell != "" {
		bad("publish-releases-repo: the verification of the published release must run exactly %q whenever the job publishes", latestVerifyRun)
	}
	if run := pub.Steps[upload].Run; !hasLine(run, latestSameLine) || !hasLine(run, `if [ "${same}" != "yes" ]; then`) {
		bad("publish-releases-repo: a signed release may be left in place only after %q answers yes", latestSameLine)
	}
	run := pub.Steps[install].Run
	check, put := strings.Index(run, latestSupersededRun), strings.Index(run, "gh api -X PUT")
	if !hasLine(run, "set -euo pipefail") || check < 0 || put < check || !hasLine(run, `if [ "${superseded}" != "no" ]; then`) {
		bad("publish-releases-repo: install.sh must not replace a newer release's installer (%q before the commit)", latestSupersededRun)
	}
	return out
}

func TestReleaseWorkflowMarksLatestOnlyWhenPublished(t *testing.T) {
	for _, v := range latestViolations([]byte(readReleaseWorkflow(t))) {
		t.Error(v)
	}
}

func TestReleaseWorkflowLatestGuardCatchesViolations(t *testing.T) {
	const (
		createInput = "          make_latest: " + latestCreateInput + "\n"
		signedName  = "      - name: Mark the release latest\n"
		unsigned    = "      - name: Mark the release latest (no signed manifest is configured)\n"
		verifyName  = "      - name: Verify the published release\n"
		uploadName  = "      - name: Upload to the releases repo\n"
	)
	replace := func(old, new string, n int) func(*testing.T, string) string {
		return func(t *testing.T, s string) string { return nth(t, s, old, new, n) }
	}
	// block cuts the step that starts with the line header, up to the next step, job or the end.
	block := func(t *testing.T, s, header string) (rest, step string) {
		i := strings.Index(s, header)
		if i < 0 {
			t.Fatalf("no step %q", header)
		}
		j := len(s)
		for _, next := range []string{"\n      - ", "\n  # ───"} {
			if k := strings.Index(s[i+1:], next); k >= 0 && i+1+k+1 < j {
				j = i + 1 + k + 1
			}
		}
		return s[:i] + s[j:], s[i:j]
	}
	remove := func(header string) func(*testing.T, string) string {
		return func(t *testing.T, s string) string { rest, _ := block(t, s, header); return rest }
	}
	moveBefore := func(header, before string) func(*testing.T, string) string {
		return func(t *testing.T, s string) string {
			rest, step := block(t, s, header)
			return nth(t, rest, before, strings.TrimRight(step, "\n")+"\n\n"+before, 0)
		}
	}
	mutations := map[string]func(*testing.T, string) string{
		"created latest by default":    replace(createInput, "", 0),
		"created latest":               replace(createInput, "          make_latest: true\n", 0),
		"empty output means latest":    replace(createInput, "          make_latest: ${{ steps.holds.outputs.holds }}\n", 0),
		"created as a draft":           replace(createInput, createInput+"          draft: true\n", 0),
		"holds step removed":           remove("      - name: Does this release already hold latest\n"),
		"holds always true":            replace(latestHoldsLine, `holds=true`, 0),
		"fallback create is latest":    replace("--latest=false ", "", 0),
		"publish never marks":          remove(signedName),
		"unsigned never marks":         remove(unsigned),
		"marked after a failure":       replace(signedName+"        if: "+sshsigGate, signedName+"        if: always()", 0),
		"mark failure ignored":         replace(signedName, signedName+"        continue-on-error: true\n", 0),
		"mark result swallowed":        replace("run: "+latestMarkRun+"\n", "run: "+latestMarkRun+" || true\n", 1),
		"marked before the upload":     moveBefore(signedName, uploadName),
		"marked before the check":      moveBefore(signedName, verifyName),
		"marked while signing is used": replace("if: "+latestUnsignedIf, "if: always()", 0),
		"marked by the release job":    replace("if: "+latestUnsignedIf, "if: env.TOKEN_SET == 'true'", 0),
		"step after the mark": func(t *testing.T, s string) string {
			return strings.TrimRight(s, "\n") + "\n\n      - name: later\n        run: \"true\"\n"
		},
		"edit elsewhere":                   replace(uploadName, "      - name: early\n        run: gh release edit x --latest\n"+uploadName, 0),
		"no published check":               remove(verifyName),
		"published check ignored":          replace(latestVerifyRun, latestVerifyRun+" || true", 0),
		"check before the upload":          moveBefore(verifyName, uploadName),
		"signed release skipped unchecked": replace(latestSameLine, "same=yes", 0),
		"older installer published":        replace(latestSupersededRun, "superseded=no", 0),
		"publish without signing": replace("needs: [version, release-manifest, release, sign-manifest]",
			"needs: [version, release-manifest, release]", 0),
		"publish runs after failures": replace("  publish-releases-repo:\n", "  publish-releases-repo:\n    if: always()\n", 0),
		"release job loses TOKEN_SET": replace("      TOKEN_SET: "+latestTokenSet+"\n", "", 0),
	}
	base := readReleaseWorkflow(t)
	if v := latestViolations([]byte(base)); len(v) != 0 {
		t.Fatalf("unmutated workflow already violates: %v", v)
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			mutated := mutate(t, base)
			var probe any
			if err := yaml.Unmarshal([]byte(mutated), &probe); err != nil {
				t.Fatalf("the mutation broke the YAML, so it proves nothing: %v", err)
			}
			if v := latestViolations([]byte(mutated)); len(v) == 0 {
				t.Error("guard did not flag this violation")
			}
		})
	}
}
