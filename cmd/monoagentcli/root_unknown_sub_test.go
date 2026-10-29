package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/account/accounttest"
)

func TestUnknownSubcommandListsAvailable(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"org", "show", "x"})
	root.SilenceUsage = true
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "available:") || !strings.Contains(err.Error(), "logs") {
		t.Fatalf("want error listing subcommands, got %v", err)
	}
}

func TestUnknownCommandSuggestsClosestName(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"wrkflow", "list"}, `Did you mean: workflow?`},
		{[]string{"workflow", "lsit"}, `Did you mean: list?`},
	} {
		root := newRootCmd()
		root.SetArgs(tc.args)
		err := root.Execute()
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "available:") {
			t.Errorf("%v: want %q and the available list, got %v", tc.args, tc.want, err)
		}
	}
}

func TestUnknownCommandWithoutCloseNameHasNoSuggestion(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"zzzzzzzz"})
	err := root.Execute()
	if err == nil || strings.Contains(err.Error(), "Did you mean") || !strings.Contains(err.Error(), "available:") {
		t.Fatalf("want the available list and no suggestion, got %v", err)
	}
}

// An unknown subcommand is input the parser refuses, like an unknown flag: exit
// 3, and with --json the invalid_input object, at the root, in a group and in a
// nested group.
func TestUnknownSubcommandIsInvalidInput(t *testing.T) {
	accounttest.Install(t, accounttest.Dormant)
	freshHome(t)
	for _, args := range [][]string{
		{"nosuchtop"},
		{"task", "nosuchsub"},
		{"workflow", "nosuchsub"},
		{"chat", "history", "nosuchsub"},
	} {
		code, stdout, stderr := runMain(t, append([]string{"--json"}, args...)...)
		var out struct{ Code, Error string }
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Errorf("%v: stdout %q is not JSON: %v", args, stdout, err)
			continue
		}
		if code != 3 || out.Code != "invalid_input" || !strings.Contains(out.Error, "unknown command") || !strings.Contains(stderr, "unknown command") {
			t.Errorf("%v: exit %d, code %q, error %q, stderr %q", args, code, out.Code, out.Error, stderr)
		}
	}
}
