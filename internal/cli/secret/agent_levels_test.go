package secret

import (
	"strings"
	"testing"
)

func TestVaultAgentLevelsCLI(t *testing.T) {
	db := newSecretCLITestDB(t)
	profileP1(t, db)
	if _, err := runSecretCmd(t, db, "add", "--name", "level-password", "--value", "level-secret-value"); err != nil {
		t.Fatal(err)
	}
	for _, level := range []string{"strict", "use", "reveal", "strict"} {
		args := []string{"agent-access", level}
		if level != "strict" {
			args = append(args, "--yes")
		}
		out, err := runSecretCmd(t, db, args...)
		if err != nil || !strings.Contains(out, `"level": "`+level+`"`) {
			t.Fatalf("%s: %s %v", level, out, err)
		}
		t.Setenv("CLAUDECODE", "1")
		out, err = runSecretCmd(t, db, "reveal", "level-password", "--reveal")
		if level == "reveal" {
			if err != nil || !strings.Contains(out, "level-secret-value") {
				t.Fatalf("reveal: %s %v", out, err)
			}
		} else if err == nil || strings.Contains(out, "level-secret-value") {
			t.Fatalf("%s leaked: %s %v", level, out, err)
		}
		if _, err := runSecretCmd(t, db, "agent-access", "reveal", "--yes"); err == nil {
			t.Fatal("agent changed policy")
		}
		t.Setenv("CLAUDECODE", "")
	}
}
