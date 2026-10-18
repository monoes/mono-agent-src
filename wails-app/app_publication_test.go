//go:build !windows

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicationBindingsUseProfileScopedCLI(t *testing.T) {
	log := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, `echo "$*" >> '`+log+`'
case "$*" in
 *"publication list"*) echo '[]' ;;
 *"publication get"*) echo '{"id":"p1"}' ;;
 *"publication stats"*) echo '{"total":0,"by_platform":{},"by_kind":{}}' ;;
esac
`))
	a := newTestApp(t)
	a.ctx = context.Background()
	a.setActiveProfileID("work")
	if got := strings.TrimSpace(a.ListPublications("hello", "x", "post", "w1", "a1", "2026-10-01", "2026-10-06", 51, 50)); got != "[]" {
		t.Fatalf("list = %q", got)
	}
	if got := a.GetPublication("p1"); !strings.Contains(got, `"id":"p1"`) {
		t.Fatalf("get = %q", got)
	}
	if got := a.GetPublicationStats(); !strings.Contains(got, `"total":0`) {
		t.Fatalf("stats = %q", got)
	}
	want := []string{
		"--profile work --json publication list --limit 51 --offset 50 --search hello --platform x --kind post --workflow w1 --agent a1 --since 2026-10-01 --until 2026-10-06",
		"--profile work --json publication get p1",
		"--profile work --json publication stats",
	}
	if got := loggedArgs(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("argv = %v, want %v", got, want)
	}
}

func TestPublicationBindingReturnsCLIError(t *testing.T) {
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, "echo 'database unavailable' >&2; exit 1\n"))
	a := newTestApp(t)
	a.ctx = context.Background()
	if got := a.GetPublicationStats(); !strings.Contains(got, `"error"`) || !strings.Contains(got, "database unavailable") {
		t.Fatalf("stats = %q", got)
	}
}
