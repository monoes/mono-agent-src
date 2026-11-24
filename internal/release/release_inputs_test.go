package release

import (
	"os"
	"strings"
	"testing"
)

// r25-R25-07: the environment-protection check only warns, never fails the release, and holds no secret.
func TestReleaseWorkflowEnvironmentCheckIsWarningOnly(t *testing.T) {
	wf := readReleaseWorkflow(t)
	i := strings.Index(wf, "- name: Check the release environment has required reviewers")
	if i < 0 {
		t.Fatal("release.yml: no environment-protection check step")
	}
	step := wf[i:]
	if j := strings.Index(step[3:], "\n      - "); j >= 0 {
		step = step[:j+3]
	}
	for _, want := range []string{"continue-on-error: true", "environments/release", "::warning::", "exit 0"} {
		if !strings.Contains(step, want) {
			t.Errorf("step lacks %q", want)
		}
	}
	for _, bad := range []string{"exit 1", "::error::", "secrets."} {
		if strings.Contains(step, bad) {
			t.Errorf("step must not contain %q", bad)
		}
	}
}

// r05 F5 (static: docker may be unavailable): the image is stamped with the version and build date as release.yml does.
func TestDockerfileStampsVersionAndBuildDate(t *testing.T) {
	raw, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	df := string(raw)
	for _, want := range []string{"ARG VERSION=", "ARG BUILD_DATE=", "-X main.version=${VERSION}", "-X main.buildDate=${BUILD_DATE}", "USER monoagent"} {
		if !strings.Contains(df, want) {
			t.Errorf("Dockerfile lacks %q", want)
		}
	}
	if strings.Contains(df, "devaccount") && !strings.Contains(df, "never use devaccount") {
		t.Error("Dockerfile must not build with the devaccount tag")
	}
}

// r05-D4: the compose example offers a passphrase that is not on the data volume.
func TestComposeOffersHostPassphrase(t *testing.T) {
	raw, err := os.ReadFile("../../docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "/run/secrets/monoagent_keyring_passphrase") || !strings.Contains(string(raw), "guards an accidental copy") {
		t.Error("docker-compose.yml: host-passphrase example or the same-volume caveat is missing")
	}
}
