package release

import (
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// install.sh needs manifest.json.sshsig wherever openssl cannot verify Ed25519 (stock macOS), and
// it fails closed without it. These checks pin that the signing job proves the file as install.sh
// will read it, that it is published, and that install.sh reaches the releases repository only
// after a release that carries it.

type sshsigStep struct {
	Uses            string         `yaml:"uses"`
	Run             string         `yaml:"run"`
	If              string         `yaml:"if"`
	Shell           string         `yaml:"shell"`
	With            map[string]any `yaml:"with"`
	ContinueOnError any            `yaml:"continue-on-error"`
}

type sshsigJob struct {
	ContinueOnError any          `yaml:"continue-on-error"`
	Steps           []sshsigStep `yaml:"steps"`
}

const (
	sshsigGate         = "steps.gate.outputs.enabled == 'true'"
	installPublishIf   = sshsigGate + " && github.repository != env.RELEASES_REPO"
	sshsigVerifyHelper = "scripts/verify-sshsig.sh"
	sshsigBootstrapIf  = `if [ "${BOOTSTRAP}" = "true" ] && ! grep -q "\"${keyid} " internal/release/keys.go; then`
)

func hasLine(run, line string) bool {
	for _, l := range strings.Split(run, "\n") {
		if strings.TrimSpace(l) == line {
			return true
		}
	}
	return false
}

// stepsWith returns the indexes of the steps whose run contains needle.
func stepsWith(job sshsigJob, needle string) []int {
	var out []int
	for i, st := range job.Steps {
		if strings.Contains(st.Run, needle) {
			out = append(out, i)
		}
	}
	return out
}

// sshsigViolations returns every broken rule about the SSH signature in the release workflow.
func sshsigViolations(raw []byte) []string {
	var out []string
	bad := func(format string, a ...any) { out = append(out, fmt.Sprintf(format, a...)) }
	var wf struct {
		Jobs map[string]sshsigJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		return []string{"workflow does not parse: " + err.Error()}
	}

	// verifier checks the one verification step of a job and returns its index, or -1.
	verifier := func(jobName, wantLine string) int {
		job := wf.Jobs[jobName]
		if job.ContinueOnError != nil {
			bad("%s: continue-on-error on the job", jobName)
		}
		idx := stepsWith(job, sshsigVerifyHelper)
		if len(idx) != 1 {
			bad("%s: %d steps run %s, want exactly 1", jobName, len(idx), sshsigVerifyHelper)
			return -1
		}
		st := job.Steps[idx[0]]
		if st.If != sshsigGate || st.ContinueOnError != nil || st.Shell != "" {
			bad("%s: the verification step must run whenever the job signs and must fail it (if %q)", jobName, st.If)
		}
		if !hasLine(st.Run, "set -euo pipefail") || !hasLine(st.Run, wantLine) {
			bad("%s: the verification step must run %q under set -euo pipefail", jobName, wantLine)
		}
		for _, ignore := range []string{"||", "set +e"} {
			if strings.Contains(st.Run, ignore) {
				bad("%s: the verification step contains %q", jobName, ignore)
			}
		}
		if strings.Contains(st.Run, "--pubkey-file") && !hasLine(st.Run, sshsigBootstrapIf) {
			bad("%s: the derived key may be used only in the bootstrap branch for an unpinned key", jobName)
		}
		return idx[0]
	}

	sign := wf.Jobs["sign-manifest"]
	signed := stepsWith(sign, "-key-env")
	verified := verifier("sign-manifest",
		"bash scripts/verify-sshsig.sh --installer install.sh signed/manifest.json signed/manifest.json.sshsig")
	artifact := -1
	for i, st := range sign.Steps {
		if st.With["name"] == "signed-manifest" && strings.Contains(st.Uses, "upload-artifact") {
			artifact = i
			if !hasLine(fmt.Sprint(st.With["path"]), "signed/manifest.json.sshsig") {
				bad("sign-manifest: the signed-manifest artifact lacks signed/manifest.json.sshsig")
			}
		}
	}
	if len(signed) != 1 || artifact < 0 || verified < 0 || !(signed[0] < verified && verified < artifact) {
		bad("sign-manifest: want sign, then verify the SSH signature, then upload (steps %v, %d, %d)", signed, verified, artifact)
	}

	pub := wf.Jobs["publish-releases-repo"]
	checked := verifier("publish-releases-repo",
		"bash scripts/verify-sshsig.sh --installer install.sh upload/manifest.json upload/manifest.json.sshsig")
	upload := stepsWith(pub, "gh release upload")
	if len(upload) != 1 || checked < 0 || checked >= upload[0] {
		bad("publish-releases-repo: want the SSH signature verified before the upload (steps %d, %v)", checked, upload)
	} else if pub.Steps[upload[0]].ContinueOnError != nil {
		bad("publish-releases-repo: continue-on-error on the upload")
	}

	installs := 0
	for jobName, job := range wf.Jobs {
		for _, i := range stepsWith(job, "contents/install.sh") {
			installs++
			st := job.Steps[i]
			if jobName != "publish-releases-repo" || len(upload) != 1 || i <= upload[0] {
				bad("%s step %d publishes install.sh before the signed release assets are uploaded", jobName, i+1)
			}
			if st.If != installPublishIf || st.ContinueOnError != nil {
				bad("%s step %d: install.sh publication must require every earlier step to pass (if %q)", jobName, i+1, st.If)
			}
		}
	}
	if installs != 1 {
		bad("%d steps publish install.sh, want exactly 1", installs)
	}
	return out
}

func TestReleaseWorkflowPublishesVerifiedSSHSignature(t *testing.T) {
	for _, v := range sshsigViolations([]byte(readReleaseWorkflow(t))) {
		t.Error(v)
	}
}

func TestReleaseWorkflowSSHSignatureGuardCatchesViolations(t *testing.T) {
	const (
		signLine = "bash scripts/verify-sshsig.sh --installer install.sh signed/manifest.json signed/manifest.json.sshsig"
		pubLine  = "bash scripts/verify-sshsig.sh --installer install.sh upload/manifest.json upload/manifest.json.sshsig"
	)
	replace := func(old, new string, n int) func(*testing.T, string) string {
		return func(t *testing.T, s string) string { return nth(t, s, old, new, n) }
	}
	// block cuts the step starting at "- name: <name>" up to the next step or job.
	block := func(t *testing.T, s, name string) (rest, step string) {
		i := strings.Index(s, "      - name: "+name+"\n")
		if i < 0 {
			t.Fatalf("no step %q", name)
		}
		j := len(s)
		for _, next := range []string{"\n      - ", "\n  # ───"} {
			if k := strings.Index(s[i+1:], next); k >= 0 && i+1+k+1 < j {
				j = i + 1 + k + 1
			}
		}
		return s[:i] + s[j:], s[i:j]
	}
	const installStep = "Publish install.sh to the releases repository"
	mutations := map[string]func(*testing.T, string) string{
		"signing job does not verify":      replace(signLine, "true", 0),
		"signing job ignores the result":   replace(signLine, signLine+" || true", 0),
		"signing job verifies other files": replace(signLine, "bash scripts/verify-sshsig.sh --pubkey-file signed/signing-pubkey.txt signed/manifest.json signed/manifest.json.sshsig", 0),
		"derived key outside bootstrap":    replace(sshsigBootstrapIf, "if true; then", 0),
		"publish ignores a bootstrap rule": replace(sshsigBootstrapIf, "if true; then", 1),
		"signature not in the artifact":    replace("            signed/manifest.json.sshsig\n", "", 0),
		"publish job does not verify":      replace(pubLine, "true", 0),
		"publish job ignores the result":   replace(pubLine, pubLine+" || :", 0),
		"upload failure ignored": replace("      - name: Upload to the releases repo\n",
			"      - name: Upload to the releases repo\n        continue-on-error: true\n", 0),
		"install.sh published after a failure": replace("if: "+installPublishIf, "if: always()", 0),
		"install.sh published before the upload": func(t *testing.T, s string) string {
			rest, step := block(t, s, installStep)
			return nth(t, rest, "      - name: Upload to the releases repo\n", step+"\n      - name: Upload to the releases repo\n", 0)
		},
		"install.sh published by the release job": func(t *testing.T, s string) string {
			rest, step := block(t, s, installStep)
			return nth(t, rest, "  # ─── 4. Sign the manifest", step+"\n  # ─── 4. Sign the manifest", 0)
		},
		"verification allowed to fail": func(t *testing.T, s string) string {
			i := strings.Index(s, signLine)
			j := strings.LastIndex(s[:i], "      - name: ")
			k := j + strings.Index(s[j:], "\n") + 1
			return s[:k] + "        continue-on-error: true\n" + s[k:]
		},
	}
	base := readReleaseWorkflow(t)
	if v := sshsigViolations([]byte(base)); len(v) != 0 {
		t.Fatalf("unmutated workflow already violates: %v", v)
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			mutated := mutate(t, base)
			var probe any
			if err := yaml.Unmarshal([]byte(mutated), &probe); err != nil {
				t.Fatalf("the mutation broke the YAML, so it proves nothing: %v", err)
			}
			if v := sshsigViolations([]byte(mutated)); len(v) == 0 {
				t.Error("guard did not flag this violation")
			}
		})
	}
}
