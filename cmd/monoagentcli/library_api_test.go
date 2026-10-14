package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apipkg"
	"github.com/monoes/mono-agent/internal/library"
)

const acmePkg = `{"$schema":"apipkg.v1","id":"acme","title":"Acme","description":"Acme widgets",
"base_url":"https://acme.test/api","operations":[{"resource":"","operation":"list","title":"List widgets","method":"GET","path":"/widgets"}]}`

// apiFixture is a library fixture whose apipkg cache follows its HOME.
func apiFixture(t *testing.T) *libFixture {
	f := newLibFixture(t)
	t.Setenv("HOME", f.home)
	apipkg.Reload()
	t.Cleanup(apipkg.Reload)
	return f
}

func (f *libFixture) apiPath(id string) string {
	return filepath.Join(f.home, ".monoagent", "apipkgs", id, "package.json")
}

func (f *libFixture) handInstallAPI(id, body string) {
	f.t.Helper()
	p := f.apiPath(id)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
	apipkg.Reload()
}

// Publish an installed API package, remove it, install it back from the
// library, and see the loader register its node type; then update it.
func TestLibraryAPIPublishInstallLoadRoundTrip(t *testing.T) {
	if _, err := apipkg.Parse([]byte(acmePkg), "acme"); err != nil {
		t.Fatalf("test package: %v", err)
	}
	f := apiFixture(t)
	f.login()
	f.handInstallAPI("acme", acmePkg)

	var pub libPublished
	f.must(&pub, "library", "publish", "api", "acme", "--tags", "widgets")
	if !pub.Created || pub.Item.Visibility != "private" || pub.Item.Owner.Username != "ada" {
		t.Fatalf("publish = %+v", pub)
	}
	item := pub.Item.ID
	stored, _ := f.fake.Get(item)
	if stored.Kind != "api" || string(stored.Data) != acmePkg || stored.Name != "Acme" {
		t.Fatalf("stored = %s %s %q", stored.Kind, stored.Data, stored.Name)
	}
	f.must(&pub, "library", "publish", "apis", "acme")
	if pub.Created || pub.Item.ID != item || pub.Item.Version != "1.0.1" {
		t.Fatalf("republish = %+v", pub)
	}

	// A file publishes as its package id: the same item again.
	file := filepath.Join(t.TempDir(), "package.json")
	if err := os.WriteFile(file, []byte(acmePkg), 0o644); err != nil {
		t.Fatal(err)
	}
	f.must(&pub, "library", "publish", "api", file)
	if pub.Created || pub.Item.ID != item {
		t.Fatalf("publish by file = %+v", pub)
	}

	if err := os.RemoveAll(filepath.Dir(f.apiPath("acme"))); err != nil {
		t.Fatal(err)
	}
	var res libInstallResult
	f.must(&res, "library", "install", "api", pub.Item.Slug, "--dry-run")
	if res.Installed || res.LocalID != "acme" || apiPackageInstalled("acme") {
		t.Fatalf("dry run = %+v", res)
	}
	f.must(&res, "library", "install", "api", pub.Item.Slug)
	if !res.Installed || res.LocalID != "acme" || len(res.Warnings) != 0 {
		t.Fatalf("install = %+v", res)
	}
	if b, err := os.ReadFile(f.apiPath("acme")); err != nil || string(b) != acmePkg {
		t.Fatalf("installed file = %s, %v", b, err)
	}
	apipkg.Reload()
	if p, ok := apipkg.ForNodeType("app.acme"); !ok || p.Source != f.apiPath("acme") {
		t.Fatalf("app.acme not loaded from the install: %+v %v", p, ok)
	}
	out, _, err := runCLI(t, f.home, "library", "install", "api", pub.Item.Slug)
	if err != nil || !strings.Contains(out, "Node type app.acme is now available") {
		t.Fatalf("human install: %v\n%s", err, out)
	}

	var list libList
	f.must(&list, "library", "list", "--kind", "api", "--scope", "mine")
	if len(list.Items) != 1 || list.Items[0].Installed == nil || list.Items[0].Installed.LocalID != "acme" {
		t.Fatalf("list = %+v", list.Items)
	}
	var inst struct{ Items []library.Record }
	f.must(&inst, "library", "installed", "--kind", "api")
	if len(inst.Items) != 1 || inst.Items[0].LocalID != "acme" {
		t.Fatalf("installed = %+v", inst)
	}

	v2 := strings.Replace(acmePkg, `"Acme widgets"`, `"Acme widgets, v2"`, 1)
	f.fake.SetArtifact(item, "2.0.0", []byte(v2))
	var up struct{ Updates []libUpdate }
	f.must(&up, "library", "update", "acme")
	if len(up.Updates) != 1 || up.Updates[0].Status != "updated" || up.Updates[0].Kind != "api" {
		t.Fatalf("update = %+v", up)
	}
	if b, _ := os.ReadFile(f.apiPath("acme")); string(b) != v2 {
		t.Fatalf("after update: %s", b)
	}
}

func TestLibraryAPIInstallRejectsInvalidPackages(t *testing.T) {
	f := apiFixture(t)
	f.login()
	cases := map[string]string{
		"broken":    `{"$schema":"apipkg.v1","id":"broken"}`,
		"traversal": strings.Replace(acmePkg, `"id":"acme"`, `"id":"../../evil"`, 1),
		"absolute":  strings.Replace(acmePkg, `"id":"acme"`, `"id":"/tmp/evil"`, 1),
	}
	for slug, body := range cases {
		f.fake.Add("monoes", "api", slug, slug, "public", "1.0.0", []byte(body), nil)
		out, err := f.run("library", "install", "api", slug)
		if exitCodeFor(err) != 3 || !strings.Contains(out, "nothing was installed") {
			t.Errorf("%s: want exit 3, got %v %s", slug, err, out)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(f.home, ".monoagent", "apipkgs")); len(entries) != 0 {
		t.Fatalf("something was written: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(f.home, "evil")); err == nil {
		t.Fatal("a traversal id escaped the install dir")
	}
	for _, id := range []string{"../x", "a/b", "A", "", ".", "x/../../y"} {
		if _, err := apiPackagePath(id); err == nil {
			t.Errorf("apiPackagePath(%q) accepted", id)
		}
	}
	if out, err := f.run("library", "publish", "api", "../../etc"); exitCodeFor(err) != 2 {
		t.Errorf("publish traversal: %v %s", err, out)
	}
	if _, _, err := readAPIPackage(filepath.Join(t.TempDir(), "missing.json")); exitCodeFor(err) != 2 {
		t.Errorf("missing file: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte(cases["broken"]), 0o644)
	if out, err := f.run("library", "publish", "api", bad); exitCodeFor(err) != 3 {
		t.Errorf("publish invalid: %v %s", err, out)
	}
}

func TestLibraryAPIInstallCollisions(t *testing.T) {
	f := apiFixture(t)
	f.login()
	f.handInstallAPI("acme", acmePkg)
	other := strings.Replace(acmePkg, `"Acme widgets"`, `"from the library"`, 1)
	f.fake.Add("eve", "api", "acme", "Acme", "public", "1.0.0", []byte(other), nil)

	out, err := f.run("library", "install", "api", "acme")
	if exitCodeFor(err) != 3 || !strings.Contains(out, "--yes") {
		t.Fatalf("over a hand-installed package: %v %s", err, out)
	}
	var res libInstallResult
	f.must(&res, "library", "install", "api", "acme", "--yes")
	if !res.Installed || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "replaced") {
		t.Fatalf("--yes = %+v", res)
	}
	// Reinstalling the same item needs no confirmation.
	f.must(&res, "library", "install", "api", "acme")

	// A second installed package for app.acme would not load.
	dup := strings.Replace(acmePkg, `"id":"acme"`, `"id":"acme2","node_type":"app.acme"`, 1)
	f.fake.Add("eve", "api", "acme2", "Acme 2", "public", "1.0.0", []byte(dup), nil)
	out, err = f.run("library", "install", "api", "acme2")
	if exitCodeFor(err) != 3 || !strings.Contains(out, "already provides app.acme") || apiPackageInstalled("acme2") {
		t.Fatalf("claim conflict: %v %s", err, out)
	}

	// Overriding a built-in catalog package is today's loader rule: allowed.
	cat, err := os.ReadFile("../../internal/apipkg/catalog/todoist.json")
	if err != nil {
		t.Fatal(err)
	}
	f.fake.Add("eve", "api", "todoist", "Todoist", "public", "1.0.0", bytes.TrimSpace(cat), nil)
	f.must(&res, "library", "install", "api", "todoist")
	apipkg.Reload()
	if p, ok := apipkg.ByID("todoist"); !res.Installed || !ok || p.Source != f.apiPath("todoist") {
		t.Fatalf("catalog override = %+v, %+v", res, p)
	}
}
