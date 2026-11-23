package release

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The release workflow holds the signing key as an environment secret. These checks parse the
// workflow and pin the properties that keep that safe, so a later edit cannot quietly widen where
// the key goes. workflowViolations is exercised against mutated copies below, so each rule is
// proven to fail when it is broken.

const signingSecret = "RELEASE_SIGNING_KEY"

type wfStep struct {
	Name  string         `yaml:"name"`
	Uses  string         `yaml:"uses"`
	Run   string         `yaml:"run"`
	Shell string         `yaml:"shell"`
	If    string         `yaml:"if"`
	Env   map[string]any `yaml:"env"`
	With  map[string]any `yaml:"with"`
}

type wfJob struct {
	Uses        string         `yaml:"uses"`
	Secrets     any            `yaml:"secrets"`
	Environment any            `yaml:"environment"`
	Env         map[string]any `yaml:"env"`
	Defaults    wfDefaults     `yaml:"defaults"`
	Steps       []wfStep       `yaml:"steps"`
}

type wfDefaults struct {
	Run struct {
		Shell string `yaml:"shell"`
	} `yaml:"run"`
}

type wfFile struct {
	Defaults wfDefaults       `yaml:"defaults"`
	On       any              `yaml:"on"`
	Env      map[string]any   `yaml:"env"`
	Jobs     map[string]wfJob `yaml:"jobs"`
}

var (
	pinnedUsesRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[0-9a-f]{40}$`)
	xtraceRe     = regexp.MustCompile(`(?m)(\bset\s+-[A-Za-z]*x|\b(ba|z|da)?sh\s+-[A-Za-z]*x|xtrace)`)
	envDumpRe    = regexp.MustCompile(`(?m)(^\s*(env|printenv|export\s+-p|declare\s+-[xp]+|set)\s*($|[|;>&])|\bprintenv\b|\benv\s*[|>])`)
	echoSecretRe = regexp.MustCompile(`(?m)\b(echo|printf)\b[^\n]*\$\{?` + signingSecret)
	// Case-insensitive, whitespace-tolerant references to the signing secret, dotted or bracketed.
	secretRefRe = regexp.MustCompile(`(?i)secrets\s*\.\s*release_signing_key|secrets\s*\[\s*['"]release_signing_key['"]\s*\]`)
	// A shell invocation or option string that turns tracing on: -x, -ex, -xe, -o xtrace, xtrace.
	shellTraceRe  = regexp.MustCompile(`(?i)(^|\s)-[a-z]*x[a-z]*(\s|$)|xtrace`)
	traceEnvKeys  = []string{"SHELLOPTS", "BASH_ENV", "BASH_XTRACEFD"}
	secretIndexRe = regexp.MustCompile(`secrets\s*\[|toJSON\(\s*secrets|secrets\s*:\s*inherit`)
)

func walkStrings(v any, f func(string)) {
	switch t := v.(type) {
	case string:
		f(t)
	case []any:
		for _, e := range t {
			walkStrings(e, f)
		}
	case map[string]any:
		for k, e := range t {
			f(k)
			walkStrings(e, f)
		}
	}
}

func envMentions(env map[string]any, needle string) bool {
	for k, v := range env {
		if strings.Contains(k, needle) || strings.Contains(fmt.Sprint(v), needle) {
			return true
		}
	}
	return false
}

func environmentName(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		if n, ok := t["name"].(string); ok {
			return n
		}
	}
	return ""
}

// workflowViolations returns every broken rule in the workflow YAML.
func workflowViolations(raw []byte) []string {
	var out []string
	bad := func(format string, a ...any) { out = append(out, fmt.Sprintf(format, a...)) }

	var wf wfFile
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		return []string{"workflow does not parse: " + err.Error()}
	}
	var tree any
	if err := yaml.Unmarshal(raw, &tree); err != nil {
		return []string{"workflow does not parse: " + err.Error()}
	}

	// Triggers: exactly a push to master.
	on, ok := wf.On.(map[string]any)
	if !ok || len(on) != 1 {
		bad("on: must be exactly a push to master, got %v", wf.On)
	} else if push, ok := on["push"].(map[string]any); !ok {
		bad("on: has no push trigger (got %v)", on)
	} else if br, _ := push["branches"].([]any); len(push) != 1 || len(br) != 1 || br[0] != "master" {
		bad("on.push must be only branches: [master], got %v", push)
	}

	// The secret and secret-wide patterns across the whole document.
	secretRefs := 0
	walkStrings(tree, func(s string) {
		secretRefs += len(secretRefRe.FindAllString(s, -1))
		if secretIndexRe.MatchString(s) {
			bad("workflow uses secrets[...], toJSON(secrets) or 'secrets: inherit': %q", s)
		}
	})
	if secretRefs != 1 {
		bad("secrets.%s is referenced %d times, want exactly 1 (the signing step's env)", signingSecret, secretRefs)
	}
	if shellTraceRe.MatchString(wf.Defaults.Run.Shell) {
		bad("workflow defaults.run.shell enables tracing: %q", wf.Defaults.Run.Shell)
	}
	badTraceEnv := func(where string, env map[string]any) {
		for k, v := range env {
			for _, tk := range traceEnvKeys {
				if strings.EqualFold(k, tk) {
					bad("%s: env sets %s=%v", where, k, v)
				}
			}
			if shellTraceRe.MatchString(fmt.Sprint(v)) && strings.Contains(strings.ToUpper(k), "SHELL") {
				bad("%s: env %s enables tracing: %v", where, k, v)
			}
		}
	}
	badTraceEnv("workflow", wf.Env)
	if envMentions(wf.Env, signingSecret) || envMentions(wf.Env, "secrets[") {
		bad("workflow-level env references the signing key")
	}

	keySteps := 0
	for jobName, job := range wf.Jobs {
		if job.Secrets != nil {
			bad("job %s passes secrets to a called workflow (%v)", jobName, job.Secrets)
		}
		if shellTraceRe.MatchString(job.Defaults.Run.Shell) {
			bad("job %s: defaults.run.shell enables tracing: %q", jobName, job.Defaults.Run.Shell)
		}
		badTraceEnv("job "+jobName, job.Env)
		if job.Uses != "" {
			bad("job %s calls a reusable workflow (%s)", jobName, job.Uses)
		}
		if envMentions(job.Env, signingSecret) || envMentions(job.Env, "secrets[") {
			bad("job %s: job-level env references the signing key", jobName)
		}
		if (jobName == "sign-manifest" || jobName == "publish-releases-repo") && environmentName(job.Environment) != "release" {
			bad("job %s must declare environment: release", jobName)
		}
		for i, st := range job.Steps {
			where := fmt.Sprintf("job %s step %d (%s)", jobName, i+1, st.Name)
			if st.Uses != "" && !pinnedUsesRe.MatchString(st.Uses) {
				bad("%s: uses %q is not pinned by a 40-hex commit SHA", where, st.Uses)
			}
			if shellTraceRe.MatchString(st.Shell) {
				bad("%s: shell enables tracing: %q", where, st.Shell)
			}
			badTraceEnv(where, st.Env)
			if xtraceRe.MatchString(st.Run) {
				bad("%s: run enables shell tracing", where)
			}
			if envDumpRe.MatchString(st.Run) {
				bad("%s: run dumps the environment", where)
			}
			if echoSecretRe.MatchString(st.Run) {
				bad("%s: run echoes the signing key variable", where)
			}
			if strings.Contains(st.Run, "${{") && strings.Contains(st.Run, "secrets.") {
				bad("%s: run interpolates a secret expression directly", where)
			}
			withKey := envMentions(st.Env, signingSecret)
			if withKey {
				keySteps++
				if jobName != "sign-manifest" {
					bad("%s: only sign-manifest may carry the signing key", where)
				}
				if got := fmt.Sprint(st.Env[signingSecret]); got != "${{ secrets."+signingSecret+" }}" {
					bad("%s: env must be exactly %s: ${{ secrets.%s }}, got %q", where, signingSecret, signingSecret, got)
				}
				if !strings.Contains(st.Run, "-key-env "+signingSecret) {
					bad("%s: has the key but its run does not invoke '-key-env %s'", where, signingSecret)
				}
				for _, banned := range []string{"go run", "go build", "go test", "go generate", "go install", "npm ", "pip "} {
					if strings.Contains(st.Run, banned) {
						bad("%s: runs %q with the key in its environment; compile first, run only the binary", where, banned)
					}
				}
				if strings.Contains(st.Run, "$"+signingSecret) || strings.Contains(st.Run, "${"+signingSecret) {
					bad("%s: run expands the key variable", where)
				}
			} else if strings.Contains(st.Run, "-key-env") {
				bad("%s: invokes -key-env but its env does not carry %s", where, signingSecret)
			}
			for k, v := range st.With {
				if strings.Contains(fmt.Sprint(v), signingSecret) {
					bad("%s: with.%s references the signing key", where, k)
				}
			}
			if strings.Contains(st.If, signingSecret) {
				bad("%s: if references the signing key", where)
			}
		}
	}
	if keySteps != 1 {
		bad("%d steps carry %s, want exactly 1", keySteps, signingSecret)
	}
	return out
}

func readReleaseWorkflow(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestReleaseWorkflowSigningKeyHandling(t *testing.T) {
	for _, v := range workflowViolations([]byte(readReleaseWorkflow(t))) {
		t.Error(v)
	}
}

// nth replaces the n-th (0-based) occurrence of old; the mutation must apply or the test is void.
func nth(t *testing.T, s, old, new string, n int) string {
	t.Helper()
	idx := -1
	from := 0
	for i := 0; i <= n; i++ {
		j := strings.Index(s[from:], old)
		if j < 0 {
			t.Fatalf("mutation anchor not found (occurrence %d): %q", n, old)
		}
		idx = from + j
		from = idx + len(old)
	}
	return s[:idx] + new + s[idx+len(old):]
}

func TestReleaseWorkflowGuardCatchesViolations(t *testing.T) {
	const jobEnvAnchor = "    environment: release\n    permissions:\n      contents: read\n"
	const signEnvKey = "          RELEASE_SIGNING_KEY: ${{ secrets.RELEASE_SIGNING_KEY }}\n"
	mutations := map[string]func(t *testing.T, s string) string{
		"pull_request trigger": func(t *testing.T, s string) string {
			return nth(t, s, "on:\n  push:", "on:\n  pull_request:\n  push:", 0)
		},
		"pull_request_target trigger": func(t *testing.T, s string) string {
			return nth(t, s, "on:\n  push:", "on:\n  pull_request_target:\n  push:", 0)
		},
		"workflow_dispatch trigger": func(t *testing.T, s string) string {
			return nth(t, s, "on:\n  push:", "on:\n  workflow_dispatch:\n  push:", 0)
		},
		"workflow_run trigger": func(t *testing.T, s string) string {
			return nth(t, s, "on:\n  push:", "on:\n  workflow_run:\n    workflows: [x]\n  push:", 0)
		},
		"push to another branch": func(t *testing.T, s string) string {
			return nth(t, s, "branches: [master]", "branches: [master, dev]", 0)
		},
		"job-level env has the key": func(t *testing.T, s string) string {
			return nth(t, s, jobEnvAnchor+"    env:\n", jobEnvAnchor+"    env:\n      LEAK: ${{ secrets.RELEASE_SIGNING_KEY }}\n", 0)
		},
		"workflow-level env has the key": func(t *testing.T, s string) string {
			return nth(t, s, "permissions:\n  contents: read\n", "env:\n  LEAK: ${{ secrets.RELEASE_SIGNING_KEY }}\npermissions:\n  contents: read\n", 0)
		},
		"workflow-level env indexes secrets": func(t *testing.T, s string) string {
			return nth(t, s, "permissions:\n  contents: read\n", "env:\n  LEAK: ${{ secrets['RELEASE_SIGNING_KEY'] }}\npermissions:\n  contents: read\n", 0)
		},
		"second step gets the key": func(t *testing.T, s string) string {
			return nth(t, s, "          BOOTSTRAP: ${{ vars.RELEASE_SIGNING_BOOTSTRAP }}\n", "          BOOTSTRAP: ${{ vars.RELEASE_SIGNING_BOOTSTRAP }}\n"+signEnvKey, 0)
		},
		"step shell bash -x": func(t *testing.T, s string) string {
			return nth(t, s, "        run: |\n          set +x\n          min_args=()", "        shell: bash -x {0}\n        run: |\n          set +x\n          min_args=()", 0)
		},
		"step shell bash -ex": func(t *testing.T, s string) string {
			return nth(t, s, "        run: |\n          set +x\n          min_args=()", "        shell: bash -ex {0}\n        run: |\n          set +x\n          min_args=()", 0)
		},
		"workflow default shell traces": func(t *testing.T, s string) string {
			return nth(t, s, "permissions:\n  contents: read\n", "defaults:\n  run:\n    shell: bash -x {0}\npermissions:\n  contents: read\n", 0)
		},
		"job default shell traces": func(t *testing.T, s string) string {
			return nth(t, s, jobEnvAnchor+"    env:\n", jobEnvAnchor+"    defaults:\n      run:\n        shell: bash -x {0}\n    env:\n", 0)
		},
		"env SHELLOPTS xtrace": func(t *testing.T, s string) string {
			return nth(t, s, "          LATEST: ${{ steps.latest.outputs.tag }}\n", "          LATEST: ${{ steps.latest.outputs.tag }}\n          SHELLOPTS: xtrace\n", 0)
		},
		"env BASH_XTRACEFD": func(t *testing.T, s string) string {
			return nth(t, s, "          LATEST: ${{ steps.latest.outputs.tag }}\n", "          LATEST: ${{ steps.latest.outputs.tag }}\n          BASH_XTRACEFD: '2'\n", 0)
		},
		"workflow env BASH_ENV": func(t *testing.T, s string) string {
			return nth(t, s, "permissions:\n  contents: read\n", "env:\n  BASH_ENV: /tmp/x\npermissions:\n  contents: read\n", 0)
		},
		"second lowercase secret reference": func(t *testing.T, s string) string {
			return nth(t, s, "          LATEST: ${{ steps.latest.outputs.tag }}\n", "          LATEST: ${{ steps.latest.outputs.tag }}\n          COPY: ${{ secrets.release_signing_key }}\n", 0)
		},
		"second spaced secret reference": func(t *testing.T, s string) string {
			return nth(t, s, "          LATEST: ${{ steps.latest.outputs.tag }}\n", "          LATEST: ${{ steps.latest.outputs.tag }}\n          COPY: ${{ secrets . Release_Signing_Key }}\n", 0)
		},
		"second bracket secret reference double quote": func(t *testing.T, s string) string {
			return nth(t, s, "          LATEST: ${{ steps.latest.outputs.tag }}\n", "          LATEST: ${{ steps.latest.outputs.tag }}\n          COPY: ${{ secrets[\"RELEASE_SIGNING_KEY\"] }}\n", 0)
		},
		"second bracket secret reference single quote": func(t *testing.T, s string) string {
			return nth(t, s, "          LATEST: ${{ steps.latest.outputs.tag }}\n", "          LATEST: ${{ steps.latest.outputs.tag }}\n          COPY: ${{ secrets['release_signing_key'] }}\n", 0)
		},
		"set -x":  func(t *testing.T, s string) string { return nth(t, s, "set +x", "set -x", 0) },
		"set -ex": func(t *testing.T, s string) string { return nth(t, s, "set -euo pipefail", "set -ex", 0) },
		"bash -x": func(t *testing.T, s string) string {
			return nth(t, s, "bash scripts/release-flatten.sh", "bash -x scripts/release-flatten.sh", 0)
		},
		"xtrace": func(t *testing.T, s string) string { return nth(t, s, "set +x", "set -o xtrace", 0) },
		"env dump": func(t *testing.T, s string) string {
			return nth(t, s, "          min_args=()\n", "          env\n          min_args=()\n", 0)
		},
		"printenv": func(t *testing.T, s string) string {
			return nth(t, s, "          min_args=()\n", "          printenv | sort\n          min_args=()\n", 0)
		},
		"echo of the variable": func(t *testing.T, s string) string {
			return nth(t, s, "          min_args=()\n", "          echo \"$RELEASE_SIGNING_KEY\"\n          min_args=()\n", 0)
		},
		"printf of the variable": func(t *testing.T, s string) string {
			return nth(t, s, "          min_args=()\n", "          printf '%s' \"${RELEASE_SIGNING_KEY}\"\n          min_args=()\n", 0)
		},
		"secret interpolated into run": func(t *testing.T, s string) string {
			return nth(t, s, "          min_args=()\n", "          x=${{ secrets.RELEASES_REPO_TOKEN }}\n          min_args=()\n", 0)
		},
		"secrets: inherit": func(t *testing.T, s string) string {
			return nth(t, s, "  test:\n    runs-on: ubuntu-latest\n", "  test:\n    secrets: inherit\n    runs-on: ubuntu-latest\n", 0)
		},
		"unpinned action": func(t *testing.T, s string) string {
			return nth(t, s, "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1", "actions/checkout@v4", 0)
		},
		"short sha action": func(t *testing.T, s string) string {
			return nth(t, s, "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e", "actions/setup-go@b7ad1da", 0)
		},
		"sign-manifest lacks environment": func(t *testing.T, s string) string {
			return nth(t, s, jobEnvAnchor, "    permissions:\n      contents: read\n", 0)
		},
		"publish lacks environment": func(t *testing.T, s string) string {
			return nth(t, s, jobEnvAnchor, "    permissions:\n      contents: read\n", 1)
		},
		"key step compiles with the key": func(t *testing.T, s string) string {
			return nth(t, s, "\"${RUNNER_TEMP}/release-manifest\" sign", "go run ./cmd/release-manifest sign", 0)
		},
		"key step without -key-env": func(t *testing.T, s string) string {
			return nth(t, s, "-key-env RELEASE_SIGNING_KEY", "-key-file k", 0)
		},
		"key removed from the signing step": func(t *testing.T, s string) string {
			return nth(t, s, signEnvKey, "", 0)
		},
	}
	base := readReleaseWorkflow(t)
	if v := workflowViolations([]byte(base)); len(v) != 0 {
		t.Fatalf("unmutated workflow already violates: %v", v)
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if v := workflowViolations([]byte(mutate(t, base))); len(v) == 0 {
				t.Error("guard did not flag this violation")
			}
		})
	}
}
