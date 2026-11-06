package doomsday

import (
	"strings"
	"testing"
)

// On a best-effort containment the CLI prints the limit with the word and never a bare "stopped".
func TestStopBestEffortPrintsTheLimitNeverBareStopped(t *testing.T) {
	r := newRig()
	r.e.stop = StopOutcome{MarkerRecorded: true, IntentRecorded: true, BestEffort: true}
	out, err := run(t, r.deps, "", "stop")
	if err != nil {
		t.Fatal(err, out)
	}
	const want = "stopped (best effort: processes that left the group are not covered)"
	if !strings.Contains(out, want) {
		t.Fatalf("output lacks %q: %q", want, out)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) == "stopped" {
			t.Fatalf("a bare stopped line in %q", out)
		}
	}
}

func TestStopBestEffortDoesNotSoftenAFailure(t *testing.T) {
	r := newRig()
	r.e.stop = StopOutcome{MarkerRecorded: true, IntentRecorded: true, BestEffort: true, Survivors: []string{"pid 7"}}
	out, err := run(t, r.deps, "", "stop")
	if err == nil || strings.Contains(out, "best effort") {
		t.Fatalf("a survivor is a failure, not a best-effort stop: err=%v out=%q", err, out)
	}
}

// The not-defended text is a function of the OS: the last bullet must be true where it is printed.
func TestNotDefendedIsTrueForEachOS(t *testing.T) {
	const refuses = "refuses to start where its processes cannot be contained"
	lin := NotDefended("linux")
	for _, want := range []string{"network", "FUSE", "WSL", "not detected"} {
		if !strings.Contains(oneLine(lin), want) {
			t.Errorf("linux text lacks %q", want)
		}
	}
	if !strings.Contains(lin, refuses) || strings.Contains(lin, "best effort") {
		t.Fatalf("linux: %s", lin)
	}
	win := oneLine(NotDefended("windows"))
	for _, want := range []string{"does NOT defend", "no isolation", "outlive a hard Stop", "Job Object", "best effort", "left the job"} {
		if !strings.Contains(win, want) {
			t.Errorf("windows text lacks %q", want)
		}
	}
	for _, want := range []string{"starts on Windows only when all its host checks", "weaker than on Linux", "undone", "in-place", "planted", "NTFS", "64-bit", "SQLite", "by design", "not re-run when a host resumes", "-wal", "each verification unit", "with SQLite's own connection closed", "no directory flush", "were not removed", "one Windows 10", "directories stay writable", "it would refuse (availability, not safety)"} {
		if !strings.Contains(win, want) {
			t.Errorf("windows text lacks %q", want)
		}
	}
	if strings.Contains(win, "not available yet") || strings.Contains(win, "does not start there") {
		t.Errorf("windows text still says the mode does not start: %s", win)
	}
	if strings.Contains(win, refuses) {
		t.Errorf("windows text keeps the Linux refusal sentence, which is not what happens there: %s", win)
	}
	// an OS with no containment says the mode does not start there.
	if other := NotDefended("plan9"); !strings.Contains(other, "does not start") || strings.Contains(other, "Job Object") {
		t.Errorf("unsupported OS: %s", other)
	}
}

// macOS says plainly that a process that left the group is not covered, and that Stop is never a bare "stopped".
func TestNotDefendedNamesTheProcessGroupLimitsOnDarwin(t *testing.T) {
	mac := oneLine(NotDefended("darwin"))
	for _, want := range []string{"does NOT defend", "process group", "best effort", "setsid", "setpgid", "double-forks", "NOT covered",
		"stopped (best effort: processes that left the group are not covered)", "never a bare",
		"starts on macOS only when all its host checks", "kqueue", "weaker than on Linux", "in-place", "planted", "APFS", "by design", "not been verified", "address-space limit", "not re-run when a host resumes", "-wal", "each verification unit"} {
		if !strings.Contains(mac, want) {
			t.Errorf("darwin text lacks %q", want)
		}
	}
	if strings.Contains(mac, "does not start there") || strings.Contains(mac, "not available yet") {
		t.Errorf("darwin text still says the mode does not start: %s", mac)
	}
	if strings.Contains(mac, "refuses to start where its processes cannot be contained") || strings.Contains(mac, "Job Object") || strings.Contains(mac, "no process containment") {
		t.Errorf("darwin text keeps a sentence that is not true there: %s", mac)
	}
}

// oneLine collapses the dialog's line breaks so a phrase is found wherever the text wraps.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
