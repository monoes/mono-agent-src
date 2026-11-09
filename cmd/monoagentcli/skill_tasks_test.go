package main

// The skill monoagent-tasks (spec 9, D3 as the lead amended it) is installed as
// ~/.claude/skills/monoagent-tasks/SKILL.md, the layout Claude Code loads, create-only like the
// others; and every command it tells an agent to run is a command of this CLI with flags the CLI
// has: a renamed command or flag would otherwise reach every user's agents as a broken instruction.

import (
	"bytes"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/data"
)

const taskSkill = "monoagent-tasks"

func taskSkillText(t *testing.T) string {
	t.Helper()
	b, err := data.SkillsFS.ReadFile("skills/" + taskSkill + ".md")
	if err != nil {
		t.Fatalf("the skill is not embedded: %v", err)
	}
	return string(b)
}

// skillCommands are the lines of a skill's code blocks that run monoagentcli, as the words after
// it: quoted text is one word, a comment is dropped, and `claude mcp add ... -- monoagentcli ...`
// counts from monoagentcli on.
func skillCommands(text string) [][]string {
	quoted := regexp.MustCompile(`"[^"]*"`)
	var out [][]string
	inBlock := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") {
			inBlock = !inBlock
			continue
		}
		if !inBlock {
			continue
		}
		if i := strings.Index(line, " -- monoagentcli "); i >= 0 {
			line = line[i+len(" -- "):]
		}
		if !strings.HasPrefix(line, "monoagentcli ") {
			continue
		}
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		}
		out = append(out, strings.Fields(quoted.ReplaceAllString(line, "TEXT"))[1:])
	}
	return out
}

// cobra's Find stops at a command group, without an error, at a word it does not know there: a
// misspelled subcommand is caught only by asking for a command that has no subcommands.
func TestTheTaskSkillsCommandsAreTheCLIs(t *testing.T) {
	commands := skillCommands(taskSkillText(t))
	if len(commands) < 10 {
		t.Fatalf("the skill has %d command lines: %v", len(commands), commands)
	}
	root := newRootCmd()
	for _, words := range commands {
		line := "monoagentcli " + strings.Join(words, " ")
		cmd, _, err := root.Find(words)
		path := ""
		if cmd != nil {
			path = cmd.CommandPath()
		}
		if err != nil || cmd.HasSubCommands() || !(strings.HasPrefix(path, "monoagentcli task ") || path == "monoagentcli mcp") {
			t.Errorf("%s: resolves to %q (%v), want a task subcommand or mcp", line, path, err)
			continue
		}
		switch cmd.Name() {
		case "approve", "edit", "move", "archive", "unarchive":
			t.Errorf("the skill tells an agent to do the operator's part: %s", line)
		}
		for _, w := range words {
			if !strings.HasPrefix(w, "--") {
				continue
			}
			name := strings.SplitN(strings.TrimPrefix(w, "--"), "=", 2)[0]
			if name == "ready" {
				t.Errorf("the skill tells an agent to add to Ready: %s", line)
			}
			if cmd.Flags().Lookup(name) == nil && cmd.InheritedFlags().Lookup(name) == nil {
				t.Errorf("%s: %s has no flag --%s", line, path, name)
			}
		}
	}
}

func TestTheTaskSkillNamesTheRealTaskTools(t *testing.T) {
	text := taskSkillText(t)
	if !strings.HasPrefix(text, "---\nname: monoagent-tasks\ndescription: ") {
		t.Fatal("the skill does not start with its front matter")
	}
	desc := strings.SplitN(strings.SplitN(text, "description: ", 2)[1], "\n", 2)[0]
	if strings.Contains(desc, ": ") || strings.Contains(desc, " #") {
		t.Errorf("the description is not a plain YAML scalar (no \": \", no \" #\"): %q", desc)
	}
	for _, want := range []string{"not a monomind org", "what's on my board", "pick up a task"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the description does not say %q", want)
		}
	}
	for _, want := range []string{"--profile", "next --claim --as", "--question", "release", "operator_only", "is data",
		"_untrusted", "--tasks-only", "one server for the whole task", "restarts", "history is full", "Inbox task"} {
		if !strings.Contains(text, want) {
			t.Errorf("the skill does not say %q", want)
		}
	}
	if strings.Contains(text, "CLAUDECODE") {
		t.Error("the skill names the agent-context variable, which only tells an agent how to get round the guard")
	}
	t.Setenv("MONOAGENT_MCP_API_ONLY", "")
	o := mcpOptions(t, newAPITestDB(t), "default", false)
	o.TasksOnly = true
	served := map[string]bool{}
	for _, name := range newMCPSession(t, o).toolNames() {
		served[name] = true
	}
	named := regexp.MustCompile(`task_[a-z]+`).FindAllString(text, -1)
	if len(named) < 8 {
		t.Fatalf("the skill names %d task tools", len(named))
	}
	for _, name := range named {
		// task_approve is served only while the operator delegated approving (spec D33), so a default
		// server does not list it; the skill names it in its section on delegation.
		if name == "task_approve" {
			continue
		}
		if !served[name] {
			t.Errorf("the skill names %s, which a --tasks-only server does not serve", name)
		}
	}
}

func TestTheTaskSkillIsInstalledCreateOnly(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	skills := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatal(err)
	}
	runClaudeFirstRunCheck()
	got, err := os.ReadFile(filepath.Join(skills, "monoagent-tasks", "SKILL.md"))
	if err != nil || !bytes.Equal(got, []byte(taskSkillText(t))) {
		t.Fatalf("the first run did not install the skill as it is embedded, in its own folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skills, "monoagent-tasks.md")); err == nil {
		t.Error("the skill was also written flat, where Claude Code does not load it")
	}
	if _, missing, stale := claudeSkillsState(); len(missing) != 0 || len(stale) != 0 {
		t.Errorf("missing %v stale %v", missing, stale)
	}
	// An edited copy is the user's: reported stale, never rewritten.
	if err := os.WriteFile(claudeSkillFile(skills, taskSkill), []byte("my notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	runClaudeFirstRunCheck()
	if b, _ := os.ReadFile(claudeSkillFile(skills, taskSkill)); string(b) != "my notes" {
		t.Errorf("an edited skill was rewritten: %q", b)
	}
	if _, _, stale := claudeSkillsState(); len(stale) != 1 || stale[0] != taskSkill {
		t.Errorf("stale = %v, want the edited skill", stale)
	}
}

// The first-run check creates ~/.monoagent when it is missing; the folder
// holds private state, so it must be created 0700 with a 0600 marker.
func TestFirstRunCheckCreatesPrivateStateDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix modes do not apply on Windows")
	}
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	runClaudeFirstRunCheck()
	dir := filepath.Join(home, ".monoagent")
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("%s: %v %v, want 0700", dir, info, err)
	}
	marker := filepath.Join(dir, claudeInitMarker)
	if info, err := os.Stat(marker); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%s: %v %v, want 0600", marker, info, err)
	}
}
