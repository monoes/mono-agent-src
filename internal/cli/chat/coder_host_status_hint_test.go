package chat

import (
	"bytes"
	"strings"
	"testing"
)

// A host that is not running is told with its next step, not just the fact; the JSON status is unchanged.
func TestHostStatusNotRunningNamesTheNextStep(t *testing.T) {
	cfg := testDeps(&testConfig{DBPath: newChatCLITestDB(t), ProfileID: "default"})
	cmd := newCoderHostStatusCmd(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.HasPrefix(got, "not running\n") {
		t.Fatalf("status = %q", got)
	}
	for _, want := range []string{"coder host run", "coder sessions send", envPursuitJudge, "pursuit readiness"} {
		if !strings.Contains(got, want) {
			t.Errorf("the hint lacks %q: %q", want, got)
		}
	}
}
