package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// The `ref commands` entries for the coder host and sessions are held to the commands as
// built: a flag that is renamed or a subcommand that is invented fails here, not in an
// agent that follows the text.

func coderDocs() map[string]cmdDoc {
	out := map[string]cmdDoc{}
	for _, d := range cliDocs {
		if strings.HasPrefix(d.Name, "coder host") || strings.HasPrefix(d.Name, "coder sessions") {
			out[d.Name] = d
		}
	}
	return out
}

func TestEveryCoderHostAndSessionsCommandHasAReferenceEntry(t *testing.T) {
	docs := coderDocs()
	root := newRootCmd()
	for _, group := range [][]string{{"coder", "host"}, {"coder", "sessions"}, {"coder", "sessions", "settings"}} {
		g, _, err := root.Find(group)
		if err != nil || g == nil || g.Name() != group[len(group)-1] {
			t.Fatalf("no command %v", group)
		}
		for _, sub := range g.Commands() {
			if sub.Name() == "help" {
				continue
			}
			name := strings.Join(group, " ") + " " + sub.Name()
			if _, ok := docs[name]; !ok {
				t.Errorf("`ref commands` has no entry for `%s`", name)
			}
		}
		if _, ok := docs[strings.Join(group, " ")]; !ok {
			t.Errorf("`ref commands` has no entry for `%s`", strings.Join(group, " "))
		}
	}
}

func TestCoderReferenceEntriesNameOnlyRealCommandsAndFlags(t *testing.T) {
	root := newRootCmd()
	for name, d := range coderDocs() {
		cmd, _, err := root.Find(strings.Fields(name))
		if err != nil || cmd == nil || strings.Join(strings.Fields(cmd.CommandPath())[1:], " ") != name {
			t.Errorf("`ref commands` documents `%s`, which is not a command", name)
			continue
		}
		for _, f := range refFlagsIn(d.Flags) {
			if cmd.Flags().Lookup(f) == nil && cmd.InheritedFlags().Lookup(f) == nil {
				t.Errorf("`%s` documents --%s, which the command does not have", name, f)
			}
		}
		for _, ex := range d.Examples {
			for _, f := range refFlagsIn(ex) {
				if root.PersistentFlags().Lookup(f) == nil && cmd.Flags().Lookup(f) == nil && cmd.InheritedFlags().Lookup(f) == nil {
					t.Errorf("an example of `%s` uses --%s, which does not exist: %s", name, f, ex)
				}
			}
		}
		// every flag the real command has (other than help) is documented
		cmd.LocalFlags().VisitAll(func(fl *pflag.Flag) {
			if fl.Name != "help" && !strings.Contains(d.Flags, "--"+fl.Name) {
				t.Errorf("`%s` has --%s, which its `ref commands` entry does not document", name, fl.Name)
			}
		})
	}
}

// AGENTS.md's section on persistent coder sessions states numbers and names that live in
// the code; it is held to them so it cannot quietly drift.
func TestAgentsMdPersistentSessionsSectionMatchesTheCode(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	i := strings.Index(text, "### Persistent coder sessions")
	j := strings.Index(text, "## How AI works in mono-agent")
	if i < 0 || j < i {
		t.Fatal("AGENTS.md has no 'Persistent coder sessions' section before 'How AI works'")
	}
	section := text[i:j]
	want := func(what, s string) {
		t.Helper()
		if !strings.Contains(section, s) {
			t.Errorf("the section does not mention %s (%q)", what, s)
		}
	}
	def := coderhost.DefaultSettings()
	want("the check-in interval default", fmt.Sprintf("`checkin_minutes` (default %d", def.CheckinMinutes))
	want("the check-in limit default", fmt.Sprintf("`checkin_limit` (default %d)", def.CheckinLimit))
	want("the send lifetime", fmt.Sprintf("%d hours", int(coderhost.MaxSendAge.Hours())))
	want("the check-in lifetime", fmt.Sprintf("%d\n  minutes", int(coderhost.MaxCheckinAge.Minutes())))
	want("the queue cap", fmt.Sprintf("at most %d sends", coderhost.MaxPendingPerChat))
	want("the message cap", fmt.Sprintf("%d KiB", coderhost.MaxMessageBytes>>10))
	want("the depth", fmt.Sprintf("depth is %d", def.MaxDepth))
	want("the default mesh scope", "`"+def.MeshAccept+"` (default")
	rt := reflect.TypeOf(def)
	for k := 0; k < rt.NumField(); k++ {
		if tag := strings.Split(rt.Field(k).Tag.Get("json"), ",")[0]; tag != "" {
			want("the setting "+tag, "`"+tag+"`")
		}
	}
	root := newRootCmd()
	for _, group := range [][]string{{"coder", "host"}, {"coder", "sessions"}} {
		g, _, _ := root.Find(group)
		for _, sub := range g.Commands() {
			if sub.Name() != "help" {
				want("the command "+strings.Join(group, " ")+" "+sub.Name(), sub.Name())
			}
		}
	}
}
