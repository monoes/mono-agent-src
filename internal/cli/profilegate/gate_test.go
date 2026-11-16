package profilegate

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func twoProfiles() ([]ProfileRef, error) {
	return []ProfileRef{{ID: "a1", Name: "Work"}, {ID: "b2", Name: "Home"}}, nil
}

func TestScopedWithoutProfileIsRefusedWhenEnforcing(t *testing.T) {
	_, err := Check(find(tree(), "people", "list"), false, ModeEnforce, twoProfiles)
	if err == nil || !strings.Contains(err.Error(), "--profile") || !strings.Contains(err.Error(), "Work") {
		t.Fatalf("want an error naming --profile and the available profiles, got %v", err)
	}
}

func TestScopedWithoutProfileWarnsInWarnMode(t *testing.T) {
	w, err := Check(find(tree(), "people", "list"), false, ModeWarn, twoProfiles)
	if err != nil || !strings.Contains(w, "--profile") || !strings.Contains(w, "Home (b2)") {
		t.Fatalf("want a warning and no error, got %q / %v", w, err)
	}
}

func TestTheProfileListIsBestEffort(t *testing.T) {
	failing := func() ([]ProfileRef, error) { return nil, errors.New("no database") }
	for _, list := range []Lister{nil, failing} {
		w, err := Check(find(tree(), "people", "list"), false, ModeWarn, list)
		if err != nil || !strings.Contains(w, "--profile") || strings.Contains(w, "Profiles:") {
			t.Fatalf("want the warning without a list, got %q / %v", w, err)
		}
	}
}

func TestGlobalNeverNeedsAProfile(t *testing.T) {
	w, err := Check(find(tree(), "config", "get"), false, ModeEnforce, nil)
	if err != nil || w != "" {
		t.Fatalf("global command must pass silently, got %q / %v", w, err)
	}
}

func TestScopedWithProfilePasses(t *testing.T) {
	w, err := Check(find(tree(), "people", "list"), true, ModeEnforce, nil)
	if err != nil || w != "" {
		t.Fatalf("got %q / %v", w, err)
	}
}

func TestGroupsAndNonRunnableCommandsAreNotJudged(t *testing.T) {
	root := tree()
	group := &cobra.Command{Use: "grp", Annotations: map[string]string{GroupAnnotation: "true"}, RunE: func(*cobra.Command, []string) error { return nil }}
	group.AddCommand(&cobra.Command{Use: "leaf", Run: func(*cobra.Command, []string) {}})
	notRunnable := &cobra.Command{Use: "empty"}
	root.AddCommand(group, notRunnable)
	for _, c := range []*cobra.Command{group, notRunnable} {
		for _, mode := range []Mode{ModeWarn, ModeEnforce} {
			if w, err := Check(c, false, mode, nil); err != nil || w != "" {
				t.Fatalf("%s mode %d: got %q / %v", c.Name(), mode, w, err)
			}
		}
	}
	// A runnable command that also has subcommands (doctor, daemon) is still judged.
	worker := &cobra.Command{Use: "worker", Run: func(*cobra.Command, []string) {}}
	worker.AddCommand(&cobra.Command{Use: "sub", Run: func(*cobra.Command, []string) {}})
	root.AddCommand(worker)
	if w, _ := Check(worker, false, ModeWarn, nil); w == "" {
		t.Fatal("a command that does work must be judged even with subcommands")
	}
}

func TestHelpCompletionAndTheRootNeverNeedAProfile(t *testing.T) {
	root := tree()
	var completion *cobra.Command
	for _, name := range []string{"help", "completion", "__complete", "__completeNoDesc"} {
		c := &cobra.Command{Use: name}
		root.AddCommand(c)
		if name == "completion" {
			completion = c
		}
		if w, err := Check(c, false, ModeEnforce, nil); err != nil || w != "" {
			t.Fatalf("%s: got %q / %v", name, w, err)
		}
	}
	if w, err := Check(root, false, ModeEnforce, nil); err != nil || w != "" {
		t.Fatalf("root: got %q / %v", w, err)
	}
	sub := &cobra.Command{Use: "bash"}
	completion.AddCommand(sub)
	if w, err := Check(sub, false, ModeEnforce, nil); err != nil || w != "" {
		t.Fatalf("completion bash: got %q / %v", w, err)
	}
}

func TestGlobalWhenFlagIsGivenSkipsTheGate(t *testing.T) {
	root := tree()
	wf := leaf("workflow")
	validate := leaf("validate")
	validate.Flags().String("file", "", "")
	wf.AddCommand(validate)
	root.AddCommand(wf)

	if err := validate.Flags().Set("file", "w.json"); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []Mode{ModeWarn, ModeEnforce} {
		if w, err := Check(validate, false, mode, nil); err != nil || w != "" {
			t.Fatalf("with --file, mode %d: got %q / %v", mode, w, err)
		}
	}
	validate.Flags().Lookup("file").Changed = false
	if w, err := Check(validate, false, ModeWarn, nil); err != nil || !strings.Contains(w, "--profile") {
		t.Fatalf("with an id, warn: got %q / %v", w, err)
	}
	if _, err := Check(validate, false, ModeEnforce, nil); err == nil {
		t.Fatal("with an id, enforce: want an error")
	}
}

// The app runs `--json profile update|delete|backups ...` with no --profile
// (wails-app/profile_args.go unscopedCLIArgs): the profile is an argument, so
// the gate must let it through.
func TestProfileEditCommandsNeedNoProfileFlag(t *testing.T) {
	root := leaf("monoagentcli")
	p := leaf("profile")
	backups := leaf("backups")
	backups.AddCommand(leaf("purge"), leaf("list"))
	p.AddCommand(leaf("update"), leaf("delete"), backups)
	root.AddCommand(p)
	for _, path := range [][]string{{"profile", "update"}, {"profile", "delete"}, {"profile", "backups", "purge"}, {"profile", "backups", "list"}} {
		if w, err := Check(find(root, path...), false, ModeEnforce, nil); err != nil || w != "" {
			t.Errorf("%v without --profile: got %q / %v, want it to pass", path, w, err)
		}
	}
}

func TestNoProfilesYetPointsToProfileCreate(t *testing.T) {
	none := func() ([]ProfileRef, error) { return nil, nil }
	_, err := Check(find(tree(), "people", "list"), false, ModeEnforce, none)
	if err == nil || !strings.Contains(err.Error(), "monoagentcli profile create <name>") {
		t.Fatalf("an empty profile list needs the create hint, got %v", err)
	}
	// with profiles, or when the list is unknown, the hint would be wrong
	_, err = Check(find(tree(), "people", "list"), false, ModeEnforce, twoProfiles)
	if err == nil || strings.Contains(err.Error(), "profile create") {
		t.Fatalf("profiles exist: %v", err)
	}
	_, err = Check(find(tree(), "people", "list"), false, ModeEnforce, nil)
	if err == nil || strings.Contains(err.Error(), "profile create") {
		t.Fatalf("list unknown: %v", err)
	}
}
