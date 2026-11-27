//go:build !windows

package main

import (
	"context"
	"strings"
	"testing"
)

// The view's organisation items come from `org pending --all`, which likewise
// names no profile.
func TestHILOrgPendingAllNamesNoProfile(t *testing.T) {
	const answer = `{"v":1,"items":[{"org":"bud","kind":"question","profile_id":"p2","profile_name":"Work"}],"errors":[]}`
	bin, argsLog := chatFakeCLI(t, fakeChatReply{match: "org pending", stdout: answer})
	t.Setenv("MONOAGENTCLI_BIN", bin)
	a, _ := newCLIChatApp(t, bin)
	a.ctx = context.Background()
	a.selectForTest("work")

	if got := strings.TrimSpace(a.HILOrgPendingAll()); got != answer {
		t.Fatalf("HILOrgPendingAll = %s", got)
	}
	calls := readArgsLog(t, argsLog)
	if len(calls) != 1 || strings.TrimSpace(calls[0]) != "--json org pending --all" {
		t.Fatalf("calls = %q", calls)
	}
}

// The All profiles view shells out to `hil pending --all`, naming no profile
// (the CLI refuses --all with one), and hands its JSON back untouched.
func TestHILPendingAllNamesNoProfile(t *testing.T) {
	const answer = `[{"id":"work-1","profile_id":"p2","profile_name":"Work"}]`
	bin, argsLog := chatFakeCLI(t, fakeChatReply{match: "hil pending", stdout: answer})
	t.Setenv("MONOAGENTCLI_BIN", bin)
	a, _ := newCLIChatApp(t, bin)
	a.ctx = context.Background()
	a.selectForTest("work")

	if got := strings.TrimSpace(a.HILPendingAll()); got != answer {
		t.Fatalf("HILPendingAll = %s", got)
	}
	calls := readArgsLog(t, argsLog)
	if len(calls) != 1 || strings.TrimSpace(calls[0]) != "--json hil pending --all" {
		t.Fatalf("calls = %q", calls)
	}
}
