package gitevidence

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/testdb"
)

func refused(t *testing.T, what string, err error, want string) {
	t.Helper()
	if err == nil || errors.Is(err, tasks.ErrEvidenceNotApplicable) {
		t.Fatalf("%s: not refused: %v", what, err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("%s: %v, want it to say %q", what, err, want)
	}
}

func TestMissionFolderThatIsGoneIsRefused(t *testing.T) {
	dir, base, _, target := repoWith(t)
	gone := filepath.Join(t.TempDir(), "deleted")
	refused(t, "a folder that never existed", Checker("")(bg, scoped(gone, base, target, "p/p.go")), "no longer exists")
	if err := os.Rename(dir, dir+".moved"); err != nil {
		t.Fatal(err)
	}
	refused(t, "a folder that was removed", Checker("")(bg, scoped(dir, base, target, "p/p.go")), "no longer exists")
	// the file in its place is no folder either
	if err := os.WriteFile(dir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	refused(t, "a file in the folder's place", Checker("")(bg, scoped(dir, base, target, "p/p.go")), "no longer exists")
}

func TestMissionRepositoryRemovedAfterTheStartIsRefused(t *testing.T) {
	dir, base, _, target := repoWith(t)
	if err := os.Rename(filepath.Join(dir, ".git"), filepath.Join(t.TempDir(), "elsewhere")); err != nil {
		t.Fatal(err)
	}
	refused(t, ".git removed after the start was recorded", Checker("")(bg, scoped(dir, base, target, "p/p.go")), "was a repository when the task started")
	// a folder that was never a repository and has no start point keeps the old rule
	if err := Checker("")(bg, scoped(dir, "", target, "p/p.go")); !errors.Is(err, tasks.ErrEvidenceNotApplicable) {
		t.Fatalf("a folder that is no repository: %v", err)
	}
}

func TestMissionRelativeOrEmptyFolderIsRefused(t *testing.T) {
	_, base, _, target := repoWith(t)
	for _, d := range []string{"", ".", "relative/dir"} {
		refused(t, "dir "+d, Checker("")(bg, scoped(d, base, target, "p/p.go")), "work folder is unknown")
	}
}

func TestMissionRepositoryWithoutAStartPointIsRefused(t *testing.T) {
	dir, _, _, target := repoWith(t)
	refused(t, "no base", Checker("")(bg, scoped(dir, "", target, "p/p.go")), "no start point")
	refused(t, "no base, probe", Checker("")(bg, tasks.EvidenceCheck{Scoped: true, Dir: dir}), "no start point")
	// the recorded stand-in for a repository with no commit yet is a start point
	if err := Checker("")(bg, scoped(dir, "none", target, "p/p.go")); err != nil {
		t.Fatalf("a repository that had no commit at the start: %v", err)
	}
	// an operator's own session needs none
	if err := Checker(dir)(bg, commitCheck(target, "p/p.go")); err != nil {
		t.Fatalf("an unscoped check: %v", err)
	}
}

func TestMissionFolderSymlinkedIntoARepositorySubdirectory(t *testing.T) {
	dir, base, _, target := repoWith(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Join(dir, "p"), link); err != nil {
		t.Fatal(err)
	}
	if err := Checker("")(bg, scoped(link, base, target, "p/p.go")); err != nil {
		t.Fatalf("a symlinked folder inside the repository: %v", err)
	}
	// a symlink to a folder that is no repository keeps the old rule, whatever its own parents hold
	outside := filepath.Join(t.TempDir(), "plain")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(dir, "p", "into")
	if err := os.Symlink(outside, inside); err != nil {
		t.Fatal(err)
	}
	if err := Checker("")(bg, scoped(inside, "", target, "p/p.go")); !errors.Is(err, tasks.ErrEvidenceNotApplicable) {
		t.Fatalf("a symlink out of the repository was judged by its lexical parents: %v", err)
	}
}

// missionTask opens a mission session over a conversation row (cwd as given; when noRow the
// row is renamed away by the returned function, so the session's conversation cannot be found)
// and returns the store, the session and a claimed code task.
func missionTask(t *testing.T, cwd string, noRow bool) (*tasks.Store, tasks.Session, tasks.Task, func()) {
	t.Helper()
	h := testdb.Open(t)
	if _, err := h.DB.Exec(`INSERT INTO ai_chat_conversations (id, profile_id, backend, created_at, updated_at, mode, cwd) VALUES ('conv1','default','agent','x','x','coder',?)`, cwd); err != nil {
		t.Fatal(err)
	}
	s := tasks.NewStore(h.DB)
	s.SetEvidenceChecker(Checker(""))
	se, err := s.CreateSession(bg, "default", tasks.SessionInput{Title: "ev", Goal: "g", ConversationID: "conv1"}, human)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := s.AddSessionTask(bg, "default", se.ID, tasks.SessionTaskInput{Title: "work", Notes: notes, Acceptance: "ok", Kind: "task"}, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveSession(bg, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionClaim(bg, "default", se.ID, tk.ID, worker(se, tk), 0); err != nil {
		t.Fatal(err)
	}
	return s, se, tk, func() {
		if noRow {
			if _, err := h.DB.Exec(`UPDATE ai_chat_conversations SET id = 'gone' WHERE id = 'conv1'`); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestMissionWithoutAUsableConversationFolderRefusesCodeEvidence(t *testing.T) {
	_, _, _, target := repoWith(t)
	for name, tc := range map[string]struct {
		cwd   string
		noRow bool
	}{"relative cwd": {"some/dir", false}, "empty cwd": {"", false}, "conversation row gone": {"/x", true}} {
		t.Run(name, func(t *testing.T) {
			s, se, tk, prepare := missionTask(t, tc.cwd, tc.noRow)
			prepare()
			if err := art(t, s, se, tk, "commit", target); !errors.Is(err, tasks.ErrInvalid) {
				t.Fatalf("a commit was attached with no usable folder: %v", err)
			}
			if err := art(t, s, se, tk, "log", "build.log"); err != nil {
				t.Fatalf("a self-asserted log: %v", err)
			}
			if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, tasks.Outcome{Result: "done"}, worker(se, tk)); !errors.Is(err, tasks.ErrNoEvidence) {
				t.Fatalf("finish fell back to the old any-artifact rule: %v", err)
			}
		})
	}
}
