package coderhost

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// After the token is stashed, it is not in this process's environment and a child
// started afterwards does not see it; it is handed out once.
func TestStashedMissionTokenIsNotInheritedByAChild(t *testing.T) {
	_ = TakeMissionToken()
	t.Setenv(MissionTokenEnv, "9.nonce")
	StashMissionToken()
	if _, ok := os.LookupEnv(MissionTokenEnv); ok {
		t.Fatal("the token is still in the environment")
	}
	out, err := exec.Command("env").Output()
	if err != nil {
		t.Skip("no env binary:", err)
	}
	if strings.Contains(string(out), MissionTokenEnv) {
		t.Fatal("a child process inherited the token")
	}
	if got := TakeMissionToken(); got != "9.nonce" {
		t.Fatalf("token = %q", got)
	}
	if got := TakeMissionToken(); got != "" {
		t.Fatalf("a token was handed out twice: %q", got)
	}
}
