//go:build !windows

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// R20-13: the feed's filters go to `people messages all`, so the page does
// not fetch the latest 200 and filter them itself.
func TestGetAllPersonMessagesFilteredPassesTheFilters(t *testing.T) {
	log := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, `echo "$*" >> '`+log+`'
echo '[]'
`))
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")
	a.GetAllPersonMessagesFiltered(200, "outlook", "pending_approval", true)
	a.GetAllPersonMessagesFiltered(50, "", "", false)
	want := []string{
		"--profile work --json people messages all --limit 200 --source outlook --status pending_approval --unread",
		"--profile work --json people messages all --limit 50",
	}
	if got := loggedArgs(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("argv:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
