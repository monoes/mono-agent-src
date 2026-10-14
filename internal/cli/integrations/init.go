package integrations

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/data"
	"github.com/monoes/mono-agent/internal/statedir"
)

// ClaudeInitMarker is written after a successful first-run Claude check so we
// don't repeat the check on every subsequent invocation.
const ClaudeInitMarker = ".claude_init"

// ClaudeSkillNames are the skills distributed with monoagent. Each is embedded
// as data/skills/<name>.md and installed as ~/.claude/skills/<name>/SKILL.md
// (the folder layout Claude Code loads), and the same way into the skills
// folders of Codex, Gemini CLI and opencode (skillTargets), so that a
// session in ANY project — not just this repo — knows monoagent exists and
// how to drive it.
// record-to-action is deliberately absent: it is the prompt `record analyze`
// feeds the monomind runner, not a skill for interactive sessions.
var ClaudeSkillNames = []string{
	"action-template-generator",
	"monoagent-workflows",
	"monoagent-tasks",
	"monoagent-build",
}

// legacyClaudeSkillSHA256 are the contents earlier releases wrote as flat
// ~/.claude/skills/<name>.md files (every version of the embedded source in
// git history). A flat file is removed only when it matches one of these or
// the current embedded source; anything else is the user's and stays.
var legacyClaudeSkillSHA256 = map[string][]string{
	"action-template-generator": {"c449b34462a7c64dc790f8fa438b51ea9fd1af26c78f945c8073c34ccd956a6a"},
	"monoagent-workflows": {
		"de0cd8e93a65370fb2deea72158ae54fd6a96782a8898927f74e79b60147f37c",
		"ea17e3874149f4af46a7a467057c827b0eeb061918221d9a45915aa536b4a545",
	},
}

// ClaudeSkillFile is where a skill lives under skillsDir.
func ClaudeSkillFile(skillsDir, name string) string {
	return filepath.Join(skillsDir, name, "SKILL.md")
}

// removeLegacyClaudeSkill deletes the old flat <name>.md when it is
// byte-identical to something monoagent shipped. Best-effort.
func removeLegacyClaudeSkill(skillsDir, name string, current []byte) {
	flat := filepath.Join(skillsDir, name+".md")
	got, err := os.ReadFile(flat)
	if err != nil {
		return
	}
	sum := sha256.Sum256(got)
	hexSum := hex.EncodeToString(sum[:])
	match := bytes.Equal(got, current)
	for _, h := range legacyClaudeSkillSHA256[name] {
		if h == hexSum {
			match = true
		}
	}
	if match {
		_ = os.Remove(flat)
	}
}

// skillTarget is one coding-agent runtime's user skills folder. Each skill
// goes in <dir>/<name>/SKILL.md, the layout Claude Code, Codex, Gemini CLI
// and opencode all load.
type skillTarget struct {
	label  string
	dir    string
	claude bool // ~/.claude/skills: also clears the legacy flat files
}

// skillTargets lists the skills folders of the runtimes present on this
// machine (their home folder exists). withClaude adds Claude Code even
// without ~/.claude (`init --claude`). opencode also loads
// ~/.claude/skills, so it gets its own copy only when Claude Code's folder
// is not written.
func skillTargets(home string, withClaude bool) []skillTarget {
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	var out []skillTarget
	claude := withClaude || exists(filepath.Join(home, ".claude"))
	if claude {
		out = append(out, skillTarget{label: "Claude Code", dir: filepath.Join(home, ".claude", "skills"), claude: true})
	}
	if ch := codexHome(home); exists(ch) {
		out = append(out, skillTarget{label: "Codex", dir: filepath.Join(ch, "skills")})
	}
	if g := filepath.Join(home, ".gemini"); exists(g) {
		out = append(out, skillTarget{label: "Gemini CLI", dir: filepath.Join(g, "skills")})
	}
	if oc := filepath.Dir(opencodeConfigPath(home)); !claude && exists(oc) {
		out = append(out, skillTarget{label: "opencode", dir: filepath.Join(oc, "skills")})
	}
	return out
}

// skillRef is one skill in one target.
type skillRef struct {
	target skillTarget
	name   string
}

// String is the skill's name, with the runtime for any but Claude Code.
func (r skillRef) String() string {
	if r.target.claude {
		return r.name
	}
	return r.name + " (" + r.target.label + ")"
}

// NewInitCommand returns the `monoagent init` command.
func NewInitCommand() *cobra.Command {
	var claudeFlag bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize monoagent integrations",
		Long: `Set up monoagent integrations.

  --claude    Install monoagent's skills (action-template-generator,
              monoagent-workflows, monoagent-tasks, monoagent-build) into
              ~/.claude/skills, and into the skills folder of Codex
              (~/.codex/skills), Gemini CLI (~/.gemini/skills) and opencode
              (~/.config/opencode/skills, only without ~/.claude) when their
              folder exists, so those agents know how to drive monoagent.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !claudeFlag {
				return cmd.Help()
			}
			return InstallClaudeSkill(true)
		},
	}

	cmd.Flags().BoolVar(&claudeFlag, "claude", false, "Install monoagent's skills for Claude Code (and Codex, Gemini CLI, opencode when present)")
	return cmd
}

// InstallClaudeSkill writes every embedded skill to ~/.claude/skills and to
// the skills folder of every other runtime present, replacing what is
// there. If verbose is true, it prints status messages.
func InstallClaudeSkill(verbose bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}

	for _, t := range skillTargets(home, true) {
		if err := os.MkdirAll(t.dir, 0o755); err != nil {
			return fmt.Errorf("create skills dir: %w", err)
		}
		for _, name := range ClaudeSkillNames {
			content, err := data.SkillsFS.ReadFile("skills/" + name + ".md")
			if err != nil {
				return fmt.Errorf("read embedded skill %s: %w", name, err)
			}

			dest := ClaudeSkillFile(t.dir, name)
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return fmt.Errorf("create skill dir: %w", err)
			}
			if err := os.WriteFile(dest, content, 0o644); err != nil {
				return fmt.Errorf("write skill to %s: %w", dest, err)
			}
			if t.claude {
				removeLegacyClaudeSkill(t.dir, name, content)
			}
			if verbose {
				fmt.Printf("%s skill installed: %s\n", t.label, dest)
			}
		}
	}

	// Write the marker so first-run check doesn't repeat.
	monoagentDir := filepath.Join(home, ".monoagent")
	_ = statedir.WriteFile(filepath.Join(monoagentDir, ClaudeInitMarker), []byte("1"))

	if verbose {
		fmt.Printf("\nIn Claude Code, run /action-template-generator to generate crawling templates.\n")
		fmt.Printf("Claude now also knows how to run monoagent workflows and templates from any project:\n")
		fmt.Printf("  monoagentcli workflow templates list\n")
	}
	return nil
}

// RunClaudeFirstRunCheck installs monoagent's skills that are missing, for
// every coding-agent runtime present (~/.claude, ~/.codex, ~/.gemini or
// opencode's config folder exists). It checks the skills themselves rather
// than trusting the first-run marker, so a monoagent version that ships a
// NEW skill delivers it to existing installs too. It runs before every
// command, so it never rewrites a skill that is there: one the user
// edited, or one another monoagent binary wrote, stays as it is.
// Refreshing a stale skill is the doctor fix (integrations.claude_skills),
// which asks first. Errors are silently ignored — this is best-effort.
func RunClaudeFirstRunCheck() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	targets := skillTargets(home, false)
	if len(targets) == 0 {
		// No coding agent installed — write marker so we don't re-check.
		marker := filepath.Join(home, ".monoagent", ClaudeInitMarker)
		if _, err := os.Stat(marker); err != nil {
			_ = statedir.WriteFile(marker, []byte("0"))
		}
		return
	}

	missing, _ := skillsState(targets)
	if len(missing) == 0 {
		return
	}
	if err := installMissingSkills(home, missing); err != nil {
		return
	}

	var labels []string
	for _, r := range missing {
		if len(labels) == 0 || labels[len(labels)-1] != r.target.label {
			labels = append(labels, r.target.label)
		}
	}
	fmt.Fprintf(os.Stderr, "[monoagent] monoagent skills installed for %s.\n", strings.Join(labels, ", "))
	fmt.Fprintf(os.Stderr, "[monoagent] Your agent can now run: monoagentcli workflow templates list\n")
}

// ClaudeSkillsState compares the skills folders of the runtimes present
// (see skillTargets) with the skills embedded in this binary. claudeFound
// is false when no such runtime is installed. Names carry the runtime, as
// "<skill> (Codex)", for any but Claude Code.
func ClaudeSkillsState() (claudeFound bool, missing, stale []string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, nil, nil
	}
	targets := skillTargets(home, false)
	if len(targets) == 0 {
		return false, nil, nil
	}
	m, s := skillsState(targets)
	for _, r := range m {
		missing = append(missing, r.String())
	}
	for _, r := range s {
		stale = append(stale, r.String())
	}
	return true, missing, stale
}

func skillsState(targets []skillTarget) (missing, stale []skillRef) {
	for _, t := range targets {
		for _, name := range ClaudeSkillNames {
			want, err := data.SkillsFS.ReadFile("skills/" + name + ".md")
			if err != nil {
				continue
			}
			got, err := os.ReadFile(ClaudeSkillFile(t.dir, name))
			switch {
			case err != nil:
				missing = append(missing, skillRef{t, name})
			case !bytes.Equal(got, want):
				stale = append(stale, skillRef{t, name})
			}
		}
	}
	return missing, stale
}

// installMissingSkills writes only the named skills, leaving every skill
// already in a skills folder alone.
func installMissingSkills(home string, refs []skillRef) error {
	for _, r := range refs {
		content, err := data.SkillsFS.ReadFile("skills/" + r.name + ".md")
		if err != nil {
			return err
		}
		dest := ClaudeSkillFile(r.target.dir, r.name)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue // appeared meanwhile: it is not ours to replace
		}
		if err != nil {
			return err
		}
		_, werr := f.Write(content)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return werr
		}
		if r.target.claude {
			removeLegacyClaudeSkill(r.target.dir, r.name, content)
		}
	}
	monoagentDir := filepath.Join(home, ".monoagent")
	_ = statedir.WriteFile(filepath.Join(monoagentDir, ClaudeInitMarker), []byte("1"))
	return nil
}
