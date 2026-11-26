//go:build !windows

package main

import (
	"context"
	"strings"
	"testing"
)

// The app approves or denies one request, the one the person saw, by its id;
// it never uses the role/action form, which also resolves calls queued since.
func TestOrgApprovalBindingsResolveByRequestID(t *testing.T) {
	orig := eventsEmit
	eventsEmit = func(context.Context, string, ...interface{}) {}
	t.Cleanup(func() { eventsEmit = orig })
	argsLog, _ := connFakeCLI(t, `echo '{"v":1,"items":[]}'`)
	a := newConnTestApp(t)

	a.ApproveOrgAction("growth", "dev", "Bash", "apr-1")
	a.DenyOrgAction("growth", "-dev", "Bash", "apr-2")
	a.ListOrgPending()

	var got []string
	for _, line := range strings.Split(strings.TrimSpace(readLog(t, argsLog)), "\n") {
		// "--profile work org [--project <root>] <sub> …": the root is the test's own.
		fields := strings.Fields(line)
		if len(fields) > 4 && fields[3] == "--project" {
			fields = append(fields[:3], fields[5:]...)
		}
		got = append(got, strings.Join(fields, " "))
	}
	want := []string{
		"--profile work org approve --request=apr-1 -- growth dev Bash",
		"--profile work org deny --request=apr-2 -- growth -dev Bash",
		"--profile work org pending",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("argv =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// Without an id nothing runs.
	for _, res := range []string{a.ApproveOrgAction("growth", "dev", "Bash", ""), a.DenyOrgAction("growth", "dev", "Bash", "")} {
		if !strings.Contains(res, `"error"`) || !strings.Contains(res, "no id") {
			t.Errorf("a request without an id: %s", res)
		}
	}
	if n := len(strings.Split(strings.TrimSpace(readLog(t, argsLog)), "\n")); n != len(want) {
		t.Errorf("%d CLI runs, want %d", n, len(want))
	}
}
