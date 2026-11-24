package sociallist

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

// rig runs the commands against one scratch database; profile is switched per call.
type rig struct {
	t       *testing.T
	path    string
	profile string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, path: filepath.Join(t.TempDir(), "monoagent.db"), profile: "p1"}
	db, err := storage.NewDatabase(r.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	db.Close()
	return r
}

func (r *rig) deps() Deps {
	return Deps{
		JSONOutput: func() bool { return true },
		ProfileID:  func() string { return r.profile },
		OpenDB:     func() (*storage.Database, error) { return storage.NewDatabase(r.path) },
	}
}

// run executes `list <args>` and returns what it wrote to stdout.
func (r *rig) run(args ...string) (string, error) {
	r.t.Helper()
	old := os.Stdout
	pr, pw, err := os.Pipe()
	if err != nil {
		r.t.Fatal(err)
	}
	os.Stdout = pw
	done := make(chan string)
	go func() { b, _ := io.ReadAll(pr); done <- string(b) }()
	cmd := NewCommand(r.deps())
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	runErr := cmd.Execute()
	pw.Close()
	os.Stdout = old
	return <-done, runErr
}

func (r *rig) create(name string) string {
	r.t.Helper()
	out, err := r.run("create", "--name", name)
	if err != nil {
		r.t.Fatalf("create: %v", err)
	}
	var got struct{ ID string }
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.ID == "" {
		r.t.Fatalf("create output %q: %v", out, err)
	}
	return got.ID
}

type shown struct {
	ItemCount int `json:"item_count"`
	Items     []struct {
		Platform         string
		PlatformUsername string `json:"platform_username"`
	}
}

func (r *rig) show(id string) (shown, error) {
	r.t.Helper()
	out, err := r.run("show", id)
	var s shown
	if err == nil {
		if jerr := json.Unmarshal([]byte(out), &s); jerr != nil {
			r.t.Fatalf("show output %q: %v", out, jerr)
		}
	}
	return s, err
}

func TestListLsIsEmptyArrayWhenNoLists(t *testing.T) {
	r := newRig(t)
	out, err := r.run("ls")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("ls = %q, want []", out)
	}
}

func TestCreateAddItemShowRoundTrip(t *testing.T) {
	r := newRig(t)
	id := r.create("Leads")
	if _, err := r.run("add-item", id, "--username", "ada", "--platform", "Instagram"); err != nil {
		t.Fatal(err)
	}
	s, err := r.show(id)
	if err != nil {
		t.Fatal(err)
	}
	if s.ItemCount != 1 || len(s.Items) != 1 || s.Items[0].PlatformUsername != "ada" || s.Items[0].Platform != "instagram" {
		t.Fatalf("show = %+v, want one instagram item for ada and item_count 1", s)
	}
	out, err := r.run("ls")
	if err != nil || !strings.Contains(out, "Leads") {
		t.Fatalf("ls = %q, %v, want the list named", out, err)
	}
}

// Another profile's list is not found by id: show, add-item and delete all refuse, and a refused
// delete leaves the items alone (social_list_items has no profile_id of its own).
func TestAnotherProfilesListIsNotReachable(t *testing.T) {
	r := newRig(t)
	id := r.create("Mine")
	if _, err := r.run("add-item", id, "--username", "ada", "--platform", "x"); err != nil {
		t.Fatal(err)
	}
	r.profile = "p2"
	if out, err := r.run("ls"); err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("p2 ls = %q, %v, want []", out, err)
	}
	if _, err := r.show(id); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("p2 show err = %v, want not found", err)
	}
	if _, err := r.run("add-item", id, "--username", "eve", "--platform", "x"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("p2 add-item err = %v, want not found", err)
	}
	if _, err := r.run("delete", id); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("p2 delete err = %v, want not found", err)
	}
	r.profile = "p1"
	s, err := r.show(id)
	if err != nil || s.ItemCount != 1 || len(s.Items) != 1 {
		t.Fatalf("p1 list after p2's refused calls = %+v, %v, want the one item", s, err)
	}
}

func TestDeleteRemovesTheListAndItsItems(t *testing.T) {
	r := newRig(t)
	id := r.create("Gone")
	for _, u := range []string{"a", "b"} {
		if _, err := r.run("add-item", id, "--username", u, "--platform", "x"); err != nil {
			t.Fatal(err)
		}
	}
	out, err := r.run("delete", id)
	if err != nil || !strings.Contains(out, "2 item(s)") {
		t.Fatalf("delete = %q, %v, want 2 items removed", out, err)
	}
	if _, err := r.show(id); err == nil {
		t.Fatal("show after delete succeeded")
	}
	db, err := storage.NewDatabase(r.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.DB.QueryRow("SELECT COUNT(*) FROM social_list_items WHERE list_id = ?", id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("items left = %d, %v", n, err)
	}
}

func TestRequiredFlagsAreRefused(t *testing.T) {
	r := newRig(t)
	id := r.create("L")
	if _, err := r.run("create", "--name", ""); err == nil {
		t.Error("create with an empty name succeeded")
	}
	if _, err := r.run("add-item", id, "--username", "", "--platform", "x"); err == nil {
		t.Error("add-item with an empty username succeeded")
	}
	if _, err := r.run("add-item", id, "--username", "u", "--platform", ""); err == nil {
		t.Error("add-item with an empty platform succeeded")
	}
	if s, err := r.show(id); err != nil || s.ItemCount != 0 {
		t.Fatalf("list after refused adds = %+v, %v, want empty", s, err)
	}
}

func TestOpenErrorIsReported(t *testing.T) {
	boom := errors.New("disk on fire")
	d := Deps{
		JSONOutput: func() bool { return true },
		ProfileID:  func() string { return "p1" },
		OpenDB:     func() (*storage.Database, error) { return nil, boom },
	}
	for _, args := range [][]string{{"ls"}, {"create", "--name", "x"}, {"show", "id"}, {"delete", "id"}, {"add-item", "id", "--username", "u", "--platform", "x"}} {
		cmd := NewCommand(d)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		if err := cmd.Execute(); !errors.Is(err, boom) {
			t.Errorf("%v: err = %v, want it to wrap the open error", args, err)
		}
	}
}
