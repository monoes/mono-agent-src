package export

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/testhome"
)

func TestMain(m *testing.M) { testhome.Main(m) }

// runExport runs `export <args>` against dbPath in profile and returns what it wrote to
// os.Stdout, where the command prints.
func runExport(t *testing.T, dbPath, profile string, jsonOut bool, args ...string) string {
	t.Helper()
	cmd := NewCommand(Deps{
		JSONOutput: func() bool { return jsonOut },
		ProfileID:  func() string { return profile },
		OutputDir:  func() string { return filepath.Join(t.TempDir(), "global-out") },
		OpenDB:     func() (*storage.Database, error) { return storage.NewDatabase(dbPath) },
	})
	cmd.SetArgs(args)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	runErr := cmd.Execute()
	os.Stdout = orig
	w.Close()
	out, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatalf("export %v: %v", args, runErr)
	}
	return string(out)
}

// export writes the profile's people (only that profile's) to people_export.json in
// --output-dir, else the global --output-dir, and reports the count.
func TestExportWritesTheProfilesPeople(t *testing.T) {
	path := testdb.Path(t)
	db, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range [][3]string{{"p1", "ann", "default"}, {"p2", "bob", "default"}, {"p3", "cy", "work"}} {
		if _, err := db.DB.Exec(`INSERT INTO people (id, platform_username, platform, profile_id) VALUES (?, ?, 'LINKEDIN', ?)`, p[0], p[1], p[2]); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	dir := filepath.Join(t.TempDir(), "out")
	out := runExport(t, path, "", true, "--output-dir", dir)
	var res struct {
		OutputDir   string `json:"output_dir"`
		PeopleCount int    `json:"people_count"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.OutputDir != dir || res.PeopleCount != 2 {
		t.Fatalf("--json = %q (%v)", out, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "people_export.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Count  int              `json:"count"`
		People []storage.Person `json:"people"`
	}
	if err := json.Unmarshal(b, &file); err != nil || file.Count != 2 || len(file.People) != 2 {
		t.Fatalf("people_export.json = %s (%v)", b, err)
	}

	if out := runExport(t, path, "work", false, "--output-dir", dir); !strings.HasPrefix(out, "Exported 1 people to "+dir) {
		t.Fatalf("text = %q", out)
	}
}
