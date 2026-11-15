package chat

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/storage"
)

// The mono-agent section hands the agent a path to the CLI and the turn's
// profile, which only works at levels that use monoagentcli with the user's
// database. Coding access is told nothing about the profile, and a boxed turn
// (coder.box, coding access only) cannot reach the database at all, so it
// would be sent to commands that fail and leak the binary path for nothing.
func TestCoderMonoagentSectionFollowsAccessAndBox(t *testing.T) {
	for _, tc := range []struct {
		name   string
		box    string
		access string
		want   bool
	}{
		{"coding, no box", "", ai.AccessCoding, false},
		{"coding, compat box", BoxCompat, ai.AccessCoding, false},
		{"coding, strict box", BoxStrict, ai.AccessCoding, false},
		{"workspace, no box", "", ai.AccessWorkspace, true},
		{"full, no box", "", ai.AccessFull, true},
		{"empty access is full", "", "", true},
		{"workspace with a box is refused elsewhere, never told", BoxCompat, ai.AccessWorkspace, false},
	} {
		if got := coderWantsMonoagent(coderSettings{Box: tc.box}, tc.access); got != tc.want {
			t.Errorf("%s: wants the section = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCoderLeadPromptCarriesMonoagentOnlyWhereItWorks(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	custom, _ := filepath.EvalSymlinks(t.TempDir())
	addTestProfile(t, dbPath, "work", "Work Stuff", custom)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "work", JSONOutput: true})
	section := "mono-agent is installed on this computer"

	on := coderLeadSystemPrompt(context.Background(), db.DB, cfg, custom, true, true)
	if !strings.Contains(on, section) || !strings.Contains(on, "workflow search") || !strings.Contains(on, "--profile work") || !strings.Contains(on, "--db-path") {
		t.Errorf("workspace or full access without a box: the section is missing:\n%s", on)
	}

	off := coderLeadSystemPrompt(context.Background(), db.DB, cfg, custom, false, false)
	for _, banned := range []string{section, "workflow search", "node schema", "--db-path", "monoagentcli"} {
		if strings.Contains(off, banned) {
			t.Errorf("coding access or a boxed turn: the prompt names %q:\n%s", banned, off)
		}
	}
}
