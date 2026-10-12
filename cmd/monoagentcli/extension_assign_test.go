package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/extension"
)

func fakeBrowser(id string, version string, profiles ...string) extension.ConnInfo {
	b := extension.ConnInfo{Instance: id, Label: id, Version: version, ProfileIDs: profiles}
	if len(profiles) > 0 {
		b.Profile = profiles[0]
	}
	return b
}

func changes(plan []bindingChange) map[string][]string {
	out := map[string][]string{}
	for _, c := range plan {
		out[c.Browser.Instance] = c.Set
	}
	return out
}

func TestAssignMovesAProfileToOneBrowserAndKeepsTheOthers(t *testing.T) {
	edge := fakeBrowser("edge-0001", "1.8.0", "p-a", "p-b")
	chrome := fakeBrowser("chrome-001", "1.8.0", "p-c")
	home := fakeBrowser("home-0001", "1.8.0")
	infos := []extension.ConnInfo{edge, chrome, home}

	plan, err := planAssign(infos, "p-a", &chrome)
	if err != nil {
		t.Fatal(err)
	}
	got := changes(plan)
	want := map[string][]string{"chrome-001": {"p-c", "p-a"}, "edge-0001": {"p-b"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
	if plan[0].Browser.Instance != "chrome-001" {
		t.Fatalf("the target must be changed first, got %q", plan[0].Browser.Instance)
	}
}

func TestAssignToABrowserThatAlreadyHasItOnlyRemovesElsewhere(t *testing.T) {
	edge := fakeBrowser("edge-0001", "1.8.0", "p-a")
	chrome := fakeBrowser("chrome-001", "1.8.0", "p-a", "p-c")
	plan, err := planAssign([]extension.ConnInfo{edge, chrome}, "p-a", &edge)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string][]string{"chrome-001": {"p-c"}}; !reflect.DeepEqual(changes(plan), want) {
		t.Fatalf("plan = %v, want %v", changes(plan), want)
	}
}

func TestAssignWithNoBrowserRemovesTheProfileEverywhere(t *testing.T) {
	edge := fakeBrowser("edge-0001", "1.8.0", "p-a", "p-b")
	chrome := fakeBrowser("chrome-001", "1.7.1", "p-a")
	plan, err := planAssign([]extension.ConnInfo{edge, chrome}, "p-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"edge-0001": {"p-b"}, "chrome-001": {}}
	if !reflect.DeepEqual(changes(plan), want) {
		t.Fatalf("plan = %v, want %v", changes(plan), want)
	}
	// Nothing runs it any more: a second assign is a no-op.
	if plan, _ := planAssign([]extension.ConnInfo{fakeBrowser("edge-0001", "1.8.0", "p-b")}, "p-a", nil); len(plan) != 0 {
		t.Fatalf("expected no change, got %v", changes(plan))
	}
}

func TestAssignFromAnOlderBridgeReadsTheSingleProfile(t *testing.T) {
	// A bridge before sets reports only `profile`; a 1.7 extension runs one.
	old := extension.ConnInfo{Instance: "old-00001", Version: "1.7.1", Profile: "p-a"}
	target := fakeBrowser("new-00001", "1.8.0")
	plan, err := planAssign([]extension.ConnInfo{old, target}, "p-a", &target)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"new-00001": {"p-a"}, "old-00001": {}}
	if !reflect.DeepEqual(changes(plan), want) {
		t.Fatalf("plan = %v, want %v", changes(plan), want)
	}
}

func TestAssignRefusesToGuessTheSetOfABrowserAnOldBridgeCannotDescribe(t *testing.T) {
	// A 1.8.0 extension may run several profiles, but this bridge reports one.
	blind := extension.ConnInfo{Instance: "blind-0001", Version: "1.8.0", Profile: "p-a"}
	target := fakeBrowser("new-00001", "1.8.0")
	if _, err := planAssign([]extension.ConnInfo{blind, target}, "p-a", &target); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("err = %v, want a restart-the-bridge error", err)
	}
	if _, err := planAssign([]extension.ConnInfo{blind}, "p-z", &blind); err == nil {
		t.Fatal("must not compute a new set from a partial one")
	}
}

func TestAssignLeavesLegacyBrowsersAloneButRefusesThemAsTarget(t *testing.T) {
	legacy := extension.ConnInfo{Instance: "legacy", Legacy: true, Profile: "p-a"}
	target := fakeBrowser("new-00001", "1.8.0")
	plan, err := planAssign([]extension.ConnInfo{legacy, target}, "p-a", &target)
	if err != nil || len(plan) != 1 || plan[0].Browser.Instance != "new-00001" {
		t.Fatalf("plan = %v, err = %v", changes(plan), err)
	}
	if _, err := planAssign([]extension.ConnInfo{legacy}, "p-a", &legacy); err == nil || !strings.Contains(err.Error(), "too old") {
		t.Fatalf("legacy target: err = %v", err)
	}
}

func TestAddAndRemoveProfile(t *testing.T) {
	set := []string{"p-a"}
	if got := addProfile(set, "p-b"); !reflect.DeepEqual(got, []string{"p-a", "p-b"}) {
		t.Fatalf("add = %v", got)
	}
	if got := addProfile(set, "p-a"); !reflect.DeepEqual(got, []string{"p-a"}) {
		t.Fatalf("add of a present id = %v", got)
	}
	if set[0] != "p-a" || len(set) != 1 {
		t.Fatalf("add changed its input: %v", set)
	}
	if got := removeProfile([]string{"p-a", "p-b"}, "p-a"); !reflect.DeepEqual(got, []string{"p-b"}) {
		t.Fatalf("remove = %v", got)
	}
	if got := removeProfile([]string{"p-a"}, "p-a"); got == nil || len(got) != 0 {
		t.Fatalf("removing the last id must give an empty, non-nil set (it is sent as []): %v", got)
	}
}

func TestSharingNeedsExtension18(t *testing.T) {
	old := fakeBrowser("old-00001", "1.7.1", "p-a")
	if err := setBrowserProfiles(nil, old, []string{"p-a", "p-b"}); err == nil || !strings.Contains(err.Error(), "1.8.0") {
		t.Fatalf("sharing on 1.7.1: err = %v, want a reload-to-1.8.0 error", err)
	}
	for v, want := range map[string]bool{"1.7.1": false, "1.8.0": true, "1.10.2": true, "2.0.0": true, "": false, "junk": false} {
		if got := extension.SupportsProfileSets(v); got != want {
			t.Errorf("SupportsProfileSets(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestBrowserRowsListEveryProfile(t *testing.T) {
	rows := browserRows([]extension.ConnInfo{
		fakeBrowser("edge-0001", "1.8.0", "p-work", "p-home"),
		fakeBrowser("home-0001", "1.8.0"),
		{Instance: "old-00001", Version: "1.7.1", Profile: "p-work"},
	}, map[string]string{"p-work": "Work", "p-home": "Home"})
	if !reflect.DeepEqual(rows[0].ProfileIDs, []string{"p-work", "p-home"}) ||
		!reflect.DeepEqual(rows[0].ProfileNames, []string{"Work", "Home"}) ||
		rows[0].ProfileID != "p-work" || rows[0].ProfileName != "Work" {
		t.Fatalf("shared row = %+v", rows[0])
	}
	if rows[1].ProfileIDs == nil || len(rows[1].ProfileIDs) != 0 || rows[1].ProfileID != "" {
		t.Fatalf("unbound row = %+v (profile_ids must be [] in JSON, not null)", rows[1])
	}
	if !reflect.DeepEqual(rows[2].ProfileIDs, []string{"p-work"}) {
		t.Fatalf("older bridge row = %+v", rows[2])
	}
	var out bytes.Buffer
	printBrowserTable(&out, rows)
	if !strings.Contains(out.String(), "Work, Home") {
		t.Errorf("table should list both profiles:\n%s", out.String())
	}
}
