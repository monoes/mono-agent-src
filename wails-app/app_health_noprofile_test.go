package main

import (
	"reflect"
	"strings"
	"testing"
)

// Onboarding runs the health check before the first profile exists: the
// machine-wide builders give the scoped argv without the profile flag.
func TestHealthArgs_NoProfile(t *testing.T) {
	for _, mode := range []string{healthModeBackground, healthModeLocal, healthModeDeep, healthModeProjects} {
		got, err := machineHealthArgs(mode)
		if err != nil {
			t.Fatalf("machineHealthArgs(%s): %v", mode, err)
		}
		scoped, err := healthArgs("work", mode)
		if err != nil {
			t.Fatal(err)
		}
		if want := withoutProfileFlag(scoped); !reflect.DeepEqual(got, want) {
			t.Errorf("machineHealthArgs(%s) = %v, want %v", mode, got, want)
		}
		assertNoProfileFlag(t, got)
	}
	if _, err := machineHealthArgs("--deep"); err == nil {
		t.Error("an unknown mode must be refused")
	}
}

func TestHealthFixArgs_NoProfile(t *testing.T) {
	got := machineHealthFixArgs("monomind.install")
	tail := []string{"doctor", "fix", "--", "monomind.install"}
	if len(got) < len(tail) || !reflect.DeepEqual(got[len(got)-len(tail):], tail) {
		t.Errorf("machineHealthFixArgs = %v, want it to end with %v", got, tail)
	}
	assertNoProfileFlag(t, got)
}

// The bindings choose: the selected profile when there is one, machine-wide
// while none is selected.
func TestHealthArgv_FollowsTheSelection(t *testing.T) {
	a := &App{}
	check, err := a.healthCheckArgv(healthModeLocal)
	if err != nil {
		t.Fatal(err)
	}
	fix, err := a.healthFixArgv("monomind.install")
	if err != nil {
		t.Fatal(err)
	}
	assertNoProfileFlag(t, check)
	assertNoProfileFlag(t, fix)

	a.selectForTest("work")
	check, err = a.healthCheckArgv(healthModeLocal)
	if err != nil || !namesProfile(check, "work") {
		t.Errorf("healthCheckArgv with work selected = %v, %v", check, err)
	}
	fix, err = a.healthFixArgv("monomind.install")
	if err != nil || !namesProfile(fix, "work") {
		t.Errorf("healthFixArgv with work selected = %v, %v", fix, err)
	}
}

// withoutProfileFlag drops "--profile <id>" (and any "--profile=<id>").
func withoutProfileFlag(args []string) []string {
	out := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "--profile" {
			i++
			continue
		}
		if strings.HasPrefix(args[i], "--profile=") {
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func assertNoProfileFlag(t *testing.T, args []string) {
	t.Helper()
	for _, a := range args {
		if a == "--profile" || strings.HasPrefix(a, "--profile=") {
			t.Errorf("%v carries a profile flag", args)
		}
	}
}
