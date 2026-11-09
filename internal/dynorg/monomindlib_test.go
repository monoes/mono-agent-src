package dynorg

import (
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentBodyFromRegistryAndFolders(t *testing.T) {
	dir := t.TempDir()
	testhome.SetHome(t, t.TempDir())
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, ".monomind"), 0o755))
	must(os.MkdirAll(filepath.Join(dir, ".claude", "agents", "engineering"), 0o755))
	must(os.WriteFile(filepath.Join(dir, ".monomind", "registry.json"), []byte(`{"agents":[{"slug":"engineering-code-reviewer","name":"Code Reviewer","category":"engineering","filePath":".claude/agents/engineering/engineering-code-reviewer.md"}]}`), 0o644))
	must(os.WriteFile(filepath.Join(dir, ".claude", "agents", "engineering", "engineering-code-reviewer.md"), []byte("---\nname: Code Reviewer\n---\n\nReview like a mentor.\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, ".claude", "agents", "tester.md"), []byte("\uFEFF---\nname: Tester\ncategory: testing\n---\nTest it.\n"), 0o644))

	l := &MonomindLibrary{Bin: "/nonexistent", Cwd: dir}
	body, title, cat, err := l.AgentBody(context.Background(), "engineering-code-reviewer")
	if err != nil || body != "Review like a mentor." || title != "Code Reviewer" || cat != "engineering" {
		t.Errorf("registry agent = %q %q %q %v", body, title, cat, err)
	}
	if _, title, _, err := l.AgentBody(context.Background(), "code reviewer"); err != nil || title != "Code Reviewer" {
		t.Errorf("by name = %q %v", title, err)
	}
	body, title, cat, err = l.AgentBody(context.Background(), "tester")
	if err != nil || body != "Test it." || title != "Tester" || cat != "testing" {
		t.Errorf("folder agent = %q %q %q %v", body, title, cat, err)
	}
	if _, _, _, err := l.AgentBody(context.Background(), "nobody"); err == nil {
		t.Error("unknown agent must fail")
	}
	must(os.MkdirAll(filepath.Join(dir, ".claude", "skills", "e2e"), 0o755))
	must(os.WriteFile(filepath.Join(dir, ".claude", "skills", "e2e", "SKILL.md"), []byte("---\nname: e2e\n---\nUse a browser.\n"), 0o644))
	if text, err := l.SkillText(context.Background(), "e2e"); err != nil || text != "Use a browser." {
		t.Errorf("skill = %q %v", text, err)
	}
}

func TestStripFrontMatterWithoutOne(t *testing.T) {
	if got := stripFrontMatter("plain body\n"); got != "plain body" {
		t.Errorf("got %q", got)
	}
	if got := stripFrontMatter("---\nunterminated"); got != "---\nunterminated" {
		t.Errorf("got %q", got)
	}
}
