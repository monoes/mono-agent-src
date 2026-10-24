package chat

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/aitask"
)

// The extension's tasks (extension-fill, extension-ask) work on a page written
// by strangers, so their turns are confined like an application-apply turn:
// scoped access, the apply browser kind, and an allow-list with no submit. A
// fill that ran as a full-access coder turn would have a shell and the
// `browser submit` verb; removing the kinds from confinedVerbs makes these fail.
func TestExtensionTasksRunConfinedAsTheApplyKind(t *testing.T) {
	for _, kind := range []string{aitask.KindExtensionFill, aitask.KindExtensionAsk} {
		if !confinedKind(kind) || !ConfinedKind(kind) {
			t.Fatalf("%s is not a confined kind", kind)
		}
		withMonomindVersion(t, "2.27.0")
		r := newTaskRig(t)
		r.taskOfKind(t, "default", aitask.StatusNeedsUser, kind)
		stubSnapshot(t)
		r.turn(t, "t1", nil)
		args := r.execArgs(t)
		for _, bad := range []string{"--access", "--settings", "--tools stdio"} {
			if strings.Contains(args, bad) {
				t.Errorf("%s ran with %q: %s", kind, bad, args)
			}
		}
		for _, want := range []string{"--tools none", "--allow-bash-prefix ", "monoagentcli --profile default browser a11y",
			"monoagentcli --profile default aitask complete"} {
			if !strings.Contains(args, want) {
				t.Errorf("%s: args lack %q: %s", kind, want, args)
			}
		}
		for _, bad := range []string{"browser submit", "browser eval", "browser close", "browser permissions", "browser allow", "browser console", "browser network",
			"browser open", "browser navigate", "browser reload", "browser tabs", "browser press"} {
			if strings.Contains(args, bad) {
				t.Errorf("%s: the allow-list contains %q: %s", kind, bad, args)
			}
		}
		if got := r.kind(t); got != "apply" {
			t.Errorf("%s ran with browser kind %q, want apply", kind, got)
		}
	}
}

// The final allow-lists, exactly. Anything that leaves the queued tab (open,
// navigate, reload), lists the others (tabs) or presses a key (Enter in a field
// submits without a click) is not on them; an ask can only read.
func TestExtensionAllowListsAreExactlyThese(t *testing.T) {
	for kind, want := range map[string][]string{
		aitask.KindExtensionFill: {"browser a11y", "browser screenshot", "browser wait", "browser click", "browser type", "browser fill", "browser upload", "aitask complete", "aitask fail"},
		aitask.KindExtensionAsk:  {"browser a11y", "browser screenshot", "browser wait", "aitask complete", "aitask fail"},
	} {
		var got []string
		for _, p := range bashPrefixesFor(kind, "p1") {
			got = append(got, strings.TrimPrefix(p, "monoagentcli --profile p1 "))
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s allow-list = %v\nwant %v", kind, got, want)
		}
		if err := checkPrefixes(bashPrefixesFor(kind, "p1")); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
}

// The session instructions of the browser-capable kinds say what is and is not
// enforced, and how to hand the result back without a file tool.
func TestExtensionSessionInstructions(t *testing.T) {
	dir := stubSnapshot(t)
	for kind, want := range map[string]string{
		aitask.KindExtensionFill: "not something this session enforces",
		aitask.KindExtensionAsk:  "You only read the page",
	} {
		c := &confinement{kind: kind, taskID: "at-0123456789abcdef", workDir: workDirFor(dir), contextDir: dir, bashPrefixes: bashPrefixesFor(kind, "p1")}
		p := c.systemPrompt()
		for _, s := range []string{want, "--result-json", "monoagentcli --profile p1 browser a11y", "no file reading"} {
			if !strings.Contains(p, s) {
				t.Errorf("%s prompt lacks %q", kind, s)
			}
		}
		if strings.Contains(p, "no command that") || strings.Contains(p, "job application form") {
			t.Errorf("%s prompt claims what is not true: %.300s", kind, p)
		}
	}
}

// A confined turn whose browser-kind record could not be written (the env has
// the declared kind but no token) does not run: the browser verbs would read it
// as kind "other" and scope neither its tab nor its files.
func TestConfinedTurnIsRefusedWithoutAVouchedKind(t *testing.T) {
	kindOnly := func(kind string) func() ([]string, func(int), func()) {
		return func() ([]string, func(int), func()) {
			return []string{"MONOAGENT_AGENT_KIND=" + kind}, func(int) {}, func() {}
		}
	}
	for _, kind := range []string{"application-apply", aitask.KindExtensionFill, aitask.KindExtensionAsk, "draft-reply"} {
		withMonomindVersion(t, "2.27.0")
		oldA, oldK := issueApplyKind, issueAgentKind
		issueApplyKind = func(string) ([]string, func(int), func()) { return kindOnly("apply")() }
		issueAgentKind = func(k string) ([]string, func(int), func()) { return kindOnly(k)() }
		t.Cleanup(func() { issueApplyKind, issueAgentKind = oldA, oldK })
		r := newTaskRig(t)
		r.taskOfKind(t, "default", aitask.StatusNeedsUser, kind)
		stubSnapshot(t)
		err := r.turnErr(t, "t1", nil)
		var ce *coderError
		if !errors.As(err, &ce) || ce.code != "confinement_unenforceable" {
			t.Errorf("%s: err = %v", kind, err)
		}
		if b, _ := os.ReadFile(r.argsLog); strings.Contains(string(b), "agent exec") {
			t.Errorf("%s: the agent ran without a vouched kind", kind)
		}
		issueApplyKind, issueAgentKind = oldA, oldK
	}
}
