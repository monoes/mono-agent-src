//go:build !windows

package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A failing CLI must reach the People page as an error carrying the CLI's own
// text, not as "no people" / a count of 0.
func TestPeopleReadsReportCLIFailures(t *testing.T) {
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, `echo "database is locked" >&2
exit 1
`))
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")

	if _, err := a.GetPeople("ALL", "", 50, 0); err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("GetPeople err = %v", err)
	}
	if _, err := a.GetPeopleCount("", ""); err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("GetPeopleCount err = %v", err)
	}
	if _, err := a.GetPersonDetail("p1"); err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("GetPersonDetail err = %v", err)
	}
	if _, err := a.GetPersonInteractions("p1"); err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("GetPersonInteractions err = %v", err)
	}
}

func TestPeopleReadsReportAMissingCLI(t *testing.T) {
	t.Setenv("MONOAGENTCLI_BIN", "/nonexistent/monoagentcli-missing")
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")
	if _, err := a.GetPeople("", "", 50, 0); err == nil {
		t.Fatal("GetPeople returned no error with the CLI missing")
	}
}

// A hung CLI must stop the spinner: the read gives up with a plain message.
func TestPeopleReadsTimeOutOnAHungCLI(t *testing.T) {
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, "exec sleep 30\n"))
	old := peopleReadTimeout
	peopleReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { peopleReadTimeout = old })
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")

	start := time.Now()
	_, err := a.GetPeopleCount("", "")
	if err == nil || !strings.Contains(err.Error(), "did not answer in") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
}
