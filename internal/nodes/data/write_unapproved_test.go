package data

import (
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/fsconfine"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The file-writing nodes go through fsconfine.WritePath: in a workflow the
// user never approved they write no hidden file in the home outside the
// project folder, and nothing is written; inside the project they write.
func TestFileNodesWriteNoHiddenHomeFilesForAnUnapprovedWorkflow(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	project := filepath.Join(home, "work")
	ctx := workflow.WithUnapproved(context.Background(), project, "")
	in := workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"text": "echo hi"})}}
	write := func(path string) error {
		_, err := (&WriteBinaryFileNode{}).Execute(ctx, in, map[string]interface{}{"file_path": path, "field": "text", "encoding": "utf8"})
		return err
	}
	rc := filepath.Join(home, ".bashrc")
	if err := write(rc); !errors.Is(err, fsconfine.ErrUnapprovedPath) {
		t.Fatalf("write_binary_file to ~/.bashrc: %v", err)
	}
	csv := map[string]interface{}{"operation": "write_csv", "file_path": filepath.Join(home, ".config", "x.csv")}
	if _, err := (&SpreadsheetNode{}).Execute(ctx, in, csv); !errors.Is(err, fsconfine.ErrUnapprovedPath) {
		t.Fatalf("spreadsheet write_csv to ~/.config: %v", err)
	}
	if _, err := os.Stat(rc); err == nil {
		t.Fatal("~/.bashrc was written")
	}
	if err := write(filepath.Join(project, ".scripts", "run.sh")); err != nil {
		t.Fatalf("a hidden folder in the project: %v", err)
	}
	// Reading goes through the same rule: a key in ~/.ssh stays unread.
	key := filepath.Join(home, ".ssh", "id_ed25519")
	if err := os.MkdirAll(filepath.Dir(key), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&ReadWriteFileNode{}).Execute(ctx, workflow.NodeInput{}, map[string]interface{}{"operation": "read", "file_selector": key}); !errors.Is(err, fsconfine.ErrUnapprovedPath) {
		t.Fatalf("read_write_file read of ~/.ssh: %v", err)
	}
}

// A glob in a workflow the user never approved skips the hidden files of the
// home it matches (here a link to one: the pattern itself names no dot
// folder) and reads the rest.
func TestGlobSkipsHiddenHomeFilesForAnUnapprovedWorkflow(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	for p, body := range map[string]string{"notes/sub/a.txt": "a", ".ssh/id.txt": "key"} {
		full := filepath.Join(home, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(home, ".ssh", "id.txt"), filepath.Join(home, "notes", "sub", "b.txt")); err != nil {
		t.Fatal(err)
	}
	ctx := workflow.WithUnapproved(context.Background(), filepath.Join(home, "work"), "")
	got, err := globFiles(ctx, filepath.Join(home, "notes", "*", "*.txt"), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || filepath.Base(got[0]) != "a.txt" {
		t.Fatalf("glob read %v, want only notes/sub/a.txt", got)
	}
}

// A spreadsheet step whose operation isn't surely a read is held to the
// write rules: an expression or an unknown operation can't reach a git hook.
func TestSpreadsheetIsHeldToTheWriteRulesUnlessItSurelyReads(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	project := filepath.Join(home, "work")
	ctx := workflow.WithUnapproved(context.Background(), project, "")
	hook := filepath.Join(project, ".git", "hooks", "pre-commit")
	for _, op := range []string{"{{ $json.op }}", "write_tsv"} {
		_, err := (&SpreadsheetNode{}).Execute(ctx, workflow.NodeInput{}, map[string]interface{}{"operation": op, "file_path": hook})
		if !errors.Is(err, fsconfine.ErrUnapprovedPath) {
			t.Errorf("operation %q: %v", op, err)
		}
	}
}
