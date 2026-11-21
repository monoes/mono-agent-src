package main

import (
	"reflect"
	"strings"
	"testing"
)

// Onboarding runs the health check before the first profile exists, so an
// empty profile id builds the machine-wide doctor command.
func TestHealthArgs_NoProfile(t *testing.T) {
	for _, mode := range []string{healthModeBackground, healthModeLocal, healthModeDeep, healthModeProjects} {
		got, err := healthArgs("", mode)
		if err != nil {
			t.Fatalf("healthArgs(\"\", %s): %v", mode, err)
		}
		scoped, err := healthArgs("work", mode)
		if err != nil {
			t.Fatal(err)
		}
		want := withoutProfileFlag(scoped)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("healthArgs(\"\", %s) = %v, want %v", mode, got, want)
		}
		assertNoProfileFlag(t, got)
	}
}

func TestHealthFixArgs_NoProfile(t *testing.T) {
	got, err := healthFixArgs("", "monomind.install")
	if err != nil {
		t.Fatalf("healthFixArgs(\"\"): %v", err)
	}
	tail := []string{"doctor", "fix", "--", "monomind.install"}
	if len(got) < len(tail) || !reflect.DeepEqual(got[len(got)-len(tail):], tail) {
		t.Errorf("healthFixArgs(\"\") = %v, want it to end with %v", got, tail)
	}
	assertNoProfileFlag(t, got)
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
