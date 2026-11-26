package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// These tests run the real run blocks of release.yml (and scripts/release-latest.sh) against a
// stub `gh` that keeps the releases repository in a directory. Nothing reaches GitHub.

const stubGH = `#!/usr/bin/env bash
set -u
S="$STUB_STATE"
echo "$*" >>"$S/calls"
if [ -n "${STUB_FAIL:-}" ] && [[ "$*" == *"$STUB_FAIL"* ]]; then echo "gh: Server Error (HTTP 500)" >&2; exit 1; fi
cmd="$1 $2"
if [ "$1" = api ]; then
  case "$*" in
    *"/releases/latest"*)
      if [ -s "$S/latest" ]; then cat "$S/latest"; exit 0; fi
      echo '{"message":"Not Found"}'; echo "gh: Not Found (HTTP 404)" >&2; exit 1 ;;
    *"-X PUT"*contents/install.sh*)
      for a in "$@"; do case "$a" in content=*) printf '%s' "${a#content=}" | base64 -d >"$S/install.sh" ;; esac; done; exit 0 ;;
    *contents/install.sh*) echo abc; exit 0 ;;
  esac
  echo "stub gh: unknown: $*" >&2; exit 99
fi
tag="$3"; rel="$S/releases/$tag"; shift 3
files=(); patterns=(); dir=""; json=""; latestFlag=""
while [ $# -gt 0 ]; do
  case "$1" in
    --repo|--title|--notes|-q) shift 2 ;;
    --json) json=1; shift 2 ;;
    --dir) dir="$2"; shift 2 ;;
    --pattern) patterns+=("$2"); shift 2 ;;
    --clobber) shift ;;
    --latest) latestFlag=true; shift ;;
    --latest=false) latestFlag=false; shift ;;
    --*) echo "stub gh: unknown flag $1" >&2; exit 99 ;;
    *) files+=("$1"); shift ;;
  esac
done
case "$cmd" in
  "release view") [ -d "$rel" ] || { echo "release not found" >&2; exit 1; }; if [ -n "$json" ]; then ls "$rel"; fi ;;
  "release upload") [ -d "$rel" ] || exit 1; cp "${files[@]}" "$rel/" ;;
  "release create") [ ! -d "$rel" ] || exit 1; mkdir -p "$rel"; cp "${files[@]}" "$rel/"; [ "$latestFlag" = false ] || echo "$tag" >"$S/latest" ;;
  "release download")
    [ -d "$rel" ] || exit 1; mkdir -p "$dir"
    if [ "${#patterns[@]}" -eq 0 ]; then cp "$rel"/* "$dir/"; else for p in "${patterns[@]}"; do cp "$rel/$p" "$dir/" || exit 1; done; fi ;;
  "release edit") [ -d "$rel" ] || exit 1; if [ "$latestFlag" = true ] && [ -z "${STUB_EDIT_IGNORED:-}" ]; then echo "$tag" >"$S/latest"; fi ;;
  *) echo "stub gh: unknown: $cmd" >&2; exit 99 ;;
esac
`

// publishSteps are the steps of publish-releases-repo that touch the releases repository, in order.
var publishSteps = []string{
	"Upload to the releases repo",
	"Verify the published release",
	"Publish install.sh to the releases repository",
	"Mark the release latest",
}

type fakeReleases struct {
	t           *testing.T
	state, work string
	env         []string
}

func newFakeReleases(t *testing.T, tag string) *fakeReleases {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("release.yml runs these scripts on Linux")
	}
	root := t.TempDir()
	f := &fakeReleases{t: t, state: filepath.Join(root, "state"), work: filepath.Join(root, "work")}
	bin := filepath.Join(root, "bin")
	f.write(filepath.Join(bin, "gh"), stubGH)
	script, err := os.ReadFile("../../scripts/release-latest.sh")
	if err != nil {
		t.Fatal(err)
	}
	f.write(filepath.Join(f.work, "scripts/release-latest.sh"), string(script))
	f.write(filepath.Join(f.work, "install.sh"), "installer of "+tag+"\n")
	for _, name := range []string{"manifest.json", "manifest.json.sig", "manifest.json.sshsig", "monoagentcli-linux-amd64"} {
		f.write(filepath.Join(f.work, "upload", name), name+" of "+tag+"\n")
	}
	// The `release` job's release: the binaries, no manifest yet.
	f.write(filepath.Join(f.state, "releases", tag, "monoagentcli-linux-amd64"), "monoagentcli-linux-amd64 of "+tag+"\n")
	f.env = []string{
		"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + root, "TMPDIR=" + root, "GH_CONFIG_DIR=" + root,
		"STUB_STATE=" + f.state, "TAG=" + tag, "RELEASES_REPO=example/releases", "RELEASES_BRANCH=main",
		"GH_TOKEN=stub", "RELEASE_LATEST_RETRY_SECONDS=0",
	}
	return f
}

func (f *fakeReleases) write(path, content string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeReleases) read(rel string) string {
	raw, err := os.ReadFile(filepath.Join(f.state, rel))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func (f *fakeReleases) setLatest(tag string) { f.write(filepath.Join(f.state, "latest"), tag+"\n") }

// run executes a run block as GitHub does (bash -e) and returns its output.
func (f *fakeReleases) run(script string, extra ...string) (string, error) {
	cmd := exec.Command("bash", "-e", "-c", script)
	cmd.Dir = f.work
	cmd.Env = append(append([]string{}, f.env...), extra...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func stepRun(t *testing.T, job, name string) string {
	t.Helper()
	var wf wfFile
	if err := yaml.Unmarshal([]byte(readReleaseWorkflow(t)), &wf); err != nil {
		t.Fatal(err)
	}
	for _, st := range wf.Jobs[job].Steps {
		if st.Name == name {
			return st.Run
		}
	}
	t.Fatalf("release.yml: job %s has no step %q", job, name)
	return ""
}

// publish runs the publish job's steps, stopping at the first failure like the job does. It
// returns the name of the failed step ("" when all passed) and the combined output.
func (f *fakeReleases) publish(extra ...string) (failed, output string) {
	for _, name := range publishSteps {
		out, err := f.run(stepRun(f.t, "publish-releases-repo", name), extra...)
		output += out
		if err != nil {
			return name, output
		}
	}
	return "", output
}

func TestPublishMarksLatestAfterUpload(t *testing.T) {
	for name, previous := range map[string]string{"an older latest": "v1.9.0", "no release yet": ""} {
		t.Run(name, func(t *testing.T) {
			f := newFakeReleases(t, "v1.10.0")
			if previous != "" {
				f.setLatest(previous)
			}
			if failed, out := f.publish(); failed != "" {
				t.Fatalf("step %q failed:\n%s", failed, out)
			}
			if got := f.read("latest"); got != "v1.10.0" {
				t.Errorf("latest = %q, want v1.10.0", got)
			}
			if got := f.read("releases/v1.10.0/manifest.json.sshsig"); got == "" {
				t.Error("the signature was not uploaded")
			}
			if got := f.read("install.sh"); got != "installer of v1.10.0" {
				t.Errorf("install.sh = %q", got)
			}
		})
	}
}

func TestPublishFailureKeepsPreviousLatest(t *testing.T) {
	cases := map[string]struct{ fail, step string }{
		"upload fails":           {"release upload", "Upload to the releases repo"},
		"release unreadable":     {"--json assets", "Upload to the releases repo"},
		"published unreadable":   {"release download", "Verify the published release"},
		"latest unreadable":      {"releases/latest", "Publish install.sh to the releases repository"},
		"installer commit fails": {"-X PUT", "Publish install.sh to the releases repository"},
		"marking is rejected":    {"release edit", "Mark the release latest"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeReleases(t, "v1.10.0")
			f.setLatest("v1.9.0")
			failed, out := f.publish("STUB_FAIL=" + c.fail)
			if failed != c.step {
				t.Fatalf("failed step = %q, want %q\n%s", failed, c.step, out)
			}
			if got := f.read("latest"); got != "v1.9.0" {
				t.Errorf("latest moved to %q after a failure", got)
			}
		})
	}
}

// "Re-run failed jobs" after anything failed behind the upload must finish the release without
// uploading the signed manifest again.
func TestPublishRerunAfterUploadFinishes(t *testing.T) {
	for _, fail := range []string{"release download", "-X PUT", "release edit"} {
		t.Run(fail, func(t *testing.T) {
			f := newFakeReleases(t, "v1.10.0")
			f.setLatest("v1.9.0")
			if failed, out := f.publish("STUB_FAIL=" + fail); failed == "" {
				t.Fatalf("the injected failure did not fail the job:\n%s", out)
			}
			if err := os.Remove(filepath.Join(f.state, "calls")); err != nil {
				t.Fatal(err)
			}
			if failed, out := f.publish(); failed != "" {
				t.Fatalf("re-run: step %q failed:\n%s", failed, out)
			}
			if calls := f.read("calls"); strings.Contains(calls, "release upload") {
				t.Errorf("the re-run uploaded again:\n%s", calls)
			}
			if got := f.read("latest"); got != "v1.10.0" {
				t.Errorf("latest = %q after the re-run", got)
			}
		})
	}
}

func TestPublishRefusesToReplaceADifferentSignedManifest(t *testing.T) {
	for _, differs := range []string{"manifest.json", "manifest.json.sig", "manifest.json.sshsig"} {
		t.Run(differs, func(t *testing.T) {
			f := newFakeReleases(t, "v1.10.0")
			f.setLatest("v1.9.0")
			for _, name := range []string{"manifest.json", "manifest.json.sig", "manifest.json.sshsig"} {
				f.write(filepath.Join(f.state, "releases/v1.10.0", name), name+" of v1.10.0\n")
			}
			f.write(filepath.Join(f.state, "releases/v1.10.0", differs), "other bytes\n")
			failed, out := f.publish()
			if failed != "Upload to the releases repo" || !strings.Contains(out, "refusing to overwrite") {
				t.Fatalf("failed step = %q\n%s", failed, out)
			}
			if f.read("releases/v1.10.0/"+differs) != "other bytes" || f.read("latest") != "v1.9.0" {
				t.Error("the published release or latest changed")
			}
		})
	}
}

// A signed manifest that is up but lacks an asset must not become latest.
func TestPublishVerifiesEveryPublishedFile(t *testing.T) {
	for name, content := range map[string]string{"missing": "", "different": "tampered\n"} {
		t.Run(name, func(t *testing.T) {
			f := newFakeReleases(t, "v1.10.0")
			f.setLatest("v1.9.0")
			if out, err := f.run(stepRun(t, "publish-releases-repo", publishSteps[0])); err != nil {
				t.Fatal(out)
			}
			asset := filepath.Join(f.state, "releases/v1.10.0/monoagentcli-linux-amd64")
			if content == "" {
				if err := os.Remove(asset); err != nil {
					t.Fatal(err)
				}
			} else {
				f.write(asset, content)
			}
			failed, out := f.publish()
			if failed != "Verify the published release" || !strings.Contains(out, "monoagentcli-linux-amd64") {
				t.Fatalf("failed step = %q\n%s", failed, out)
			}
			if got := f.read("latest"); got != "v1.9.0" {
				t.Errorf("latest = %q", got)
			}
		})
	}
}

// An older run that finishes after a newer release became latest changes neither latest nor install.sh.
func TestOlderRunNeverMovesLatestBack(t *testing.T) {
	for older, newer := range map[string]string{"v1.9.0": "v1.10.0", "v1.10.9": "v1.10.10", "v1.99.99": "v2.0.0"} {
		t.Run(older+" after "+newer, func(t *testing.T) {
			f := newFakeReleases(t, older)
			f.setLatest(newer)
			f.write(filepath.Join(f.state, "install.sh"), "newer installer\n")
			failed, out := f.publish()
			if failed != "" {
				t.Fatalf("step %q failed:\n%s", failed, out)
			}
			if got := f.read("latest"); got != newer {
				t.Errorf("latest = %q, want %q", got, newer)
			}
			if got := f.read("install.sh"); got != "newer installer" {
				t.Errorf("install.sh was replaced by the older one: %q", got)
			}
			if !strings.Contains(out, "::warning::") {
				t.Errorf("no warning:\n%s", out)
			}
		})
	}
}

func TestMarkLatestFailsClosed(t *testing.T) {
	mark := "bash scripts/release-latest.sh mark"
	t.Run("latest is not a version tag", func(t *testing.T) {
		f := newFakeReleases(t, "v1.10.0")
		f.setLatest("nightly")
		if out, err := f.run(mark); err == nil || f.read("latest") != "nightly" {
			t.Fatalf("err=%v latest=%q\n%s", err, f.read("latest"), out)
		}
	})
	t.Run("the change does not take", func(t *testing.T) {
		f := newFakeReleases(t, "v1.10.0")
		f.setLatest("v1.9.0")
		out, err := f.run(mark, "STUB_EDIT_IGNORED=1")
		if err == nil || !strings.Contains(out, "::error::") {
			t.Fatalf("err=%v\n%s", err, out)
		}
	})
	t.Run("tag is not a version", func(t *testing.T) {
		f := newFakeReleases(t, "v1.10.0")
		if out, err := f.run(mark, "TAG="); err == nil {
			t.Fatalf("empty TAG accepted\n%s", out)
		}
	})
}

// The `release` job: a new or half-made release is created not latest; a re-run of the job for the
// release that already is latest keeps it so.
func TestReleaseJobHoldsStep(t *testing.T) {
	for latest, want := range map[string]string{"": "false", "v1.9.0": "false", "v1.10.0": "true", "v1.11.0": "false"} {
		f := newFakeReleases(t, "v1.10.0")
		if latest != "" {
			f.setLatest(latest)
		}
		output := filepath.Join(f.work, "output")
		if out, err := f.run(stepRun(t, "release", "Does this release already hold latest"), "GITHUB_OUTPUT="+output); err != nil {
			t.Fatalf("latest %q: %v\n%s", latest, err, out)
		}
		raw, _ := os.ReadFile(output)
		if got := strings.TrimSpace(string(raw)); got != "holds="+want {
			t.Errorf("latest %q: output %q, want holds=%s", latest, got, want)
		}
	}
	f := newFakeReleases(t, "v1.10.0")
	if out, err := f.run(stepRun(t, "release", "Does this release already hold latest"),
		"GITHUB_OUTPUT="+filepath.Join(f.work, "output"), "STUB_FAIL=releases/latest"); err == nil {
		t.Errorf("an unreadable latest must fail the step\n%s", out)
	}
}

// With no releases token there is no signing job to wait for: the release becomes latest at once.
func TestUnsignedReleaseJobMarksLatest(t *testing.T) {
	step := stepRun(t, "release", "Mark the release latest (no signed manifest is configured)")
	f := newFakeReleases(t, "v1.10.0")
	f.setLatest("v1.9.0")
	if out, err := f.run(step); err != nil || f.read("latest") != "v1.10.0" {
		t.Fatalf("err=%v latest=%q\n%s", err, f.read("latest"), out)
	}
	f.setLatest("v1.11.0")
	if out, err := f.run(step); err != nil || f.read("latest") != "v1.11.0" {
		t.Fatalf("older run: err=%v latest=%q\n%s", err, f.read("latest"), out)
	}
}
