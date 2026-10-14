package people

import (
	"encoding/json"
	"testing"

	"github.com/monoes/mono-agent/internal/peopletags"
	"github.com/spf13/cobra"
)

func runPeople(t *testing.T, cfg *testConfig, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		var cmd *cobra.Command = NewCommand(testDeps(cfg))
		cmd.SetArgs(args)
		err = cmd.Execute()
	})
	return out, err
}

func TestPeopleTagCLI(t *testing.T) {
	cfg, _ := newReviewCLITestDB(t)

	out, err := runPeople(t, cfg, "tag", "add", "p1", "hot lead", "--color", "#ff0000")
	var tag peopletags.Tag
	if err != nil || json.Unmarshal([]byte(out), &tag) != nil || tag.Color != "#ff0000" {
		t.Fatalf("add = %q, %v", out, err)
	}
	if _, err := runPeople(t, cfg, "tag", "color", "HOT LEAD", "#00ff00"); err != nil {
		t.Fatal(err)
	}
	out, err = runPeople(t, cfg, "tag", "list", "--person", "p1")
	var tags []peopletags.Tag
	if err != nil || json.Unmarshal([]byte(out), &tags) != nil || len(tags) != 1 || tags[0].Color != "#00ff00" {
		t.Fatalf("list (one tag, still an array) = %q, %v", out, err)
	}
	if _, err := runPeople(t, cfg, "tag", "remove", "p1", tag.ID); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		args []string
		code int
	}{
		{[]string{"tag", "add", "nobody", "x"}, 2},
		{[]string{"tag", "add", "p1", "x", "--color", "red"}, 3},
		{[]string{"tag", "color", "no such tag", "#fff"}, 2},
	} {
		if _, err := runPeople(t, cfg, c.args...); exitCode(err) != c.code {
			t.Errorf("%v: exit %d (%v), want %d", c.args, exitCode(err), err, c.code)
		}
	}
}

// People saved without counts or a verified flag (NULL columns, e.g. from a
// sparse import) must still list, show and export.
func TestPeopleNullColumns(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	if _, err := db.DB.Exec(`UPDATE people SET content_count = NULL, following_count = NULL, is_verified = NULL`); err != nil {
		t.Skipf("schema keeps these columns NOT NULL: %v", err)
	}
	if out, err := runPeople(t, cfg, "list"); err != nil {
		t.Fatalf("list = %q, %v", out, err)
	}
	out, err := runPeople(t, cfg, "get", "p1")
	if err != nil {
		t.Fatalf("get = %q, %v", out, err)
	}
	if p, err := db.GetPerson("p1"); err != nil || p.IsVerified || p.ContentCount != 0 {
		t.Fatalf("GetPerson = %+v, %v", p, err)
	}
	// The export half of this test stays in the CLI (TestPeopleNullColumnsExport).
}
