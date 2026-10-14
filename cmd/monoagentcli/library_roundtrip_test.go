package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/cli/librarycli"
	"github.com/monoes/mono-agent/internal/library/libraryfake"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/profiledir"
)

type libList struct {
	Items []librarycli.Item `json:"items"`
	Total int               `json:"total"`
}

type libPublished struct {
	Item struct {
		ID, Version, Visibility, Slug string
		Owner                         struct{ Username string }
	} `json:"item"`
	Created bool `json:"created"`
}

func TestLibraryWorkflowInstallAndUpdate(t *testing.T) {
	noSeed(t)
	f := newLibFixture(t)
	id := f.fake.Add("monoes", "workflow", "gemimg", "Gemini image", "official", "1.0.0",
		readRepo(t, "internal/workflow/templates/gemimg.json"), map[string]any{"required_automations": []any{"gemini"}})
	f.login()

	var res librarycli.InstallResult
	f.must(&res, "library", "install", "workflow", "gemimg", "--dry-run")
	if res.Installed || !res.DryRun {
		t.Fatalf("dry run = %+v", res)
	}
	f.must(&res, "library", "install", "workflow", "gemimg")
	if !res.Installed || res.LocalID == "" || len(res.MissingAutomations) != 1 || res.MissingAutomations[0] != "gemini" {
		t.Fatalf("install = %+v", res)
	}
	first := res.LocalID
	var list libList
	f.must(&list, "library", "list", "--kind", "workflow")
	if len(list.Items) != 1 || list.Items[0].Installed == nil || list.Items[0].Installed.LocalID != first || list.Items[0].Installed.UpdateAvailable {
		t.Fatalf("list = %+v", list.Items)
	}

	// A new version replaces the earlier import in place.
	f.fake.SetArtifact(id, "1.1.0", bytes.Replace(readRepo(t, "internal/workflow/templates/gemimg.json"),
		[]byte(`"name"`), []byte(`"description": "v2", "name"`), 1))
	var up struct{ Updates []librarycli.Update }
	f.must(&up, "library", "update", "--dry-run")
	if len(up.Updates) != 1 || up.Updates[0].Status != "available" || up.Updates[0].To != "1.1.0" {
		t.Fatalf("dry-run update = %+v", up)
	}
	f.must(&up, "library", "update")
	if up.Updates[0].Status != "updated" || up.Updates[0].LocalID != first {
		t.Fatalf("update = %+v", up)
	}
	f.must(&list, "library", "list", "--kind", "workflow")
	if in := list.Items[0].Installed; in == nil || in.Version != "1.1.0" || in.LocalID != first {
		t.Fatalf("after update: %+v", list.Items[0].Installed)
	}

	// Installing into another profile is independent.
	f.must(nil, "profile", "create", "other")
	f.login("--profile", "other")
	f.must(&list, "--profile", "other", "library", "list", "--kind", "workflow")
	if list.Items[0].Installed != nil {
		t.Fatalf("other profile sees the install: %+v", list.Items[0].Installed)
	}
}

func TestLibraryOrgInstallCollision(t *testing.T) {
	f := newLibFixture(t)
	f.fake.Add("monoes", "org", "research-team", "Research team", "official", "1.0.0", readRepo(t, "orgtemplates/research-team.json"), nil)
	f.login()

	var res librarycli.InstallResult
	f.must(&res, "library", "install", "org", "research-team")
	if !res.Installed || res.LocalID != "research-team" {
		t.Fatalf("install = %+v", res)
	}
	out, err := f.run("library", "install", "org", "research-team")
	if exitCodeFor(err) != 3 || !strings.Contains(out, "--rename") {
		t.Fatalf("collision: %v %s", err, out)
	}
	f.must(&res, "library", "install", "org", "research-team", "--rename", "research-2")
	if res.LocalID != "research-2" {
		t.Fatalf("rename = %+v", res)
	}
	f.must(&res, "library", "install", "org", "research-team", "--yes")
	if !res.Installed || len(res.Warnings) == 0 {
		t.Fatalf("replace = %+v", res)
	}

	db, err := initDB(&globalConfig{DBPath: filepath.Join(f.home, ".monoagent", "monoagent.db")})
	if err != nil {
		t.Fatal(err)
	}
	root := profiledir.Root(db.DB, "default")
	db.Close()
	d, err := orgdesign.Load(root, "research-2")
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "research-2" || d.Status != "stopped" || !strings.Contains(string(d.RunConfig["memory_namespace"]), "org:research-2") {
		t.Fatalf("renamed org = %+v", d)
	}
}

func TestLibraryPublishRoundTrips(t *testing.T) {
	f := newLibFixture(t)
	f.login()

	// Workflow: publish, then publish again = a new version of the same item.
	wfFile := filepath.Join(t.TempDir(), "wf.json")
	if err := os.WriteFile(wfFile, readRepo(t, "internal/workflow/templates/gemimg.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	var imp struct{ ID string }
	f.must(&imp, "workflow", "import", "--file", wfFile)
	var pub libPublished
	f.must(&pub, "library", "publish", "workflow", imp.ID, "--tags", "images,ai")
	if !pub.Created || pub.Item.Visibility != "private" || pub.Item.Owner.Username != "ada" {
		t.Fatalf("publish = %+v", pub)
	}
	wfItem := pub.Item.ID
	f.must(&pub, "library", "publish", "workflow", imp.ID, "--public")
	if pub.Created || pub.Item.ID != wfItem || pub.Item.Version != "1.0.1" {
		t.Fatalf("republish = %+v", pub)
	}

	// Automation: the installed package, as a .mpkg.
	f.must(&pub, "library", "publish", "automation", "hackernews")
	stored, _ := f.fake.Get(pub.Item.ID)
	zr, err := zip.NewReader(bytes.NewReader(stored.Data), int64(len(stored.Data)))
	if err != nil {
		t.Fatalf("published automation is not a zip: %v", err)
	}
	hasManifest := false
	for _, zf := range zr.File {
		hasManifest = hasManifest || zf.Name == "automation.json"
	}
	if !hasManifest || stored.Version != "1.1.0" {
		t.Fatalf("mpkg manifest %v, version %s", hasManifest, stored.Version)
	}

	// Org: installed from monoes' item, published as the user's own item.
	f.fake.Add("monoes", "org", "content-team", "Content team", "official", "1.0.0", readRepo(t, "orgtemplates/content-team.json"), nil)
	f.must(nil, "library", "install", "org", "content-team")
	f.must(&pub, "library", "publish", "org", "content-team")
	if !pub.Created || pub.Item.Owner.Username != "ada" {
		t.Fatalf("org publish = %+v", pub)
	}

	var mine libList
	f.must(&mine, "library", "list", "--scope", "mine")
	if mine.Total != 3 {
		t.Fatalf("mine = %d items", mine.Total)
	}
	for _, it := range mine.Items {
		if it.Installed == nil {
			t.Errorf("%s %s not linked to its local copy", it.Kind, it.Slug)
		}
	}

	// Round trip: the published workflow installs into a fresh profile.
	f.must(nil, "profile", "create", "fresh")
	f.login("--profile", "fresh")
	var res librarycli.InstallResult
	f.must(&res, "--profile", "fresh", "library", "install", "workflow", wfItem)
	if !res.Installed {
		t.Fatalf("install published workflow = %+v", res)
	}
}

func TestLibraryAdoptsSeededBuiltins(t *testing.T) {
	f := newLibFixture(t) // the test seed installs the built-ins, as an older release did
	id := f.fake.Add("monoes", "automation", "hackernews", "Hacker News", "official", "1.1.0", pack(t, "hackernews", ""),
		map[string]any{"automation_id": "hackernews"})
	f.login()
	var up struct{ Updates []librarycli.Update }
	f.must(&up, "library", "update", "--dry-run")
	if len(up.Updates) != 1 || up.Updates[0].LocalID != "hackernews" || up.Updates[0].Status != "up_to_date" {
		t.Fatalf("adopted = %+v", up)
	}
	row := automationRow(t, f, "hackernews")
	if row.Source != automation.SourceBuiltin || row.Library == nil || row.Library.ItemID != id {
		t.Fatalf("row = %+v", row)
	}

	f.fake.SetArtifact(id, "1.2.0", pack(t, "hackernews", "1.2.0"))
	f.must(&up, "library", "update")
	if up.Updates[0].Status != "updated" {
		t.Fatalf("update = %+v", up)
	}
	row = automationRow(t, f, "hackernews")
	if row.Version != "1.2.0" || row.Source != automation.SourceMonoes || row.Trust != automation.TrustBuiltin || row.PreviousVersion != "1.1.0" {
		t.Fatalf("updated row = %+v", row)
	}
}

// A community fork of an official package keeps the package id but gets
// another slug: it is never adopted, and replacing the more trusted copy
// needs confirmation.
func TestLibraryForkIsNotAdoptedOrSilentlyReplaced(t *testing.T) {
	f := newLibFixture(t)
	f.fake.AddUser(&libraryfake.User{ID: "u-eve", Username: "eve", Name: "Eve", Email: "eve@example.com"})
	f.fake.Add("eve", "automation", "hackernews-2", "HN fork", "public", "1.1.0", pack(t, "hackernews", ""),
		map[string]any{"automation_id": "hackernews"})
	f.login()
	var list libList
	f.must(&list, "library", "list", "--kind", "automations")
	if len(list.Items) != 1 || list.Items[0].Installed != nil {
		t.Fatalf("fork = %+v", list.Items)
	}
	if row := automationRow(t, f, "hackernews"); row.Library != nil {
		t.Fatalf("the fork was adopted: %+v", row.Library)
	}
	out, err := f.run("library", "install", "automation", "hackernews-2")
	if exitCodeFor(err) != 3 || !strings.Contains(out, "--replace") {
		t.Fatalf("fork over the built-in: %v %s", err, out)
	}
	var res librarycli.InstallResult
	f.must(&res, "library", "install", "automations/hackernews-2", "--replace")
	if res.LocalID != "hackernews" || res.Trust != automation.TrustImported {
		t.Fatalf("fork install = %+v", res)
	}
}
