package connect

import (
	"strings"
	"testing"
)

// `connect <platform>` must refuse a missing profile before it prompts or starts an OAuth
// flow. An unknown platform stands in for a real one so the test can never reach a provider:
// the profile check has to come first.
func TestConnectPlatformChecksProfileFirst(t *testing.T) {
	e := newSessionsTestEnv(t)
	_, errOut, code := e.run(t, "", "connect", "no_such_platform_for_test")
	if code == 0 || !strings.Contains(errOut, "no profile selected") {
		t.Fatalf("code %d, stderr %q; want the no-profile error before any platform work", code, errOut)
	}
}

// The global --json is persistent; `connect list` must not shadow it with a flag of its own.
func TestConnectListHasNoLocalJSONFlag(t *testing.T) {
	e := newSessionsTestEnv(t)
	cmd := NewCommand(e.deps("work", false))
	list, _, err := cmd.Find([]string{"list"})
	if err != nil {
		t.Fatal(err)
	}
	if list.Flags().Lookup("json") != nil {
		t.Fatal("connect list declares its own --json flag; it should use the global one")
	}
}
