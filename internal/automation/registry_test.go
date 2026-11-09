package automation

import (
	"bytes"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/automations"
	"github.com/monoes/mono-agent/internal/action"
)

func TestInstallDirAndContext(t *testing.T) {
	r := newReg(t)
	res, err := r.Install(acmeDir(t), InstallOptions{})
	if err != nil {
		t.Fatalf("Install: %v (%+v)", err, res)
	}
	if !res.Installed || res.Version != "1.2.0" || res.Review.Publisher != "Jane" ||
		res.Review.ActionEffects["create_contact"] != "write" || len(res.Review.Scripts) != 1 {
		t.Fatalf("result: %+v", res)
	}
	info, err := r.Info("acme-crm")
	if err != nil {
		t.Fatal(err)
	}
	if info.Trust != SourceImported || !info.Enabled || !info.Available || !info.ContainsScripts || !info.HasIcon || info.Actions != 2 {
		t.Fatalf("info: %+v", info)
	}

	p, err := r.Get("acme-crm")
	if err != nil {
		t.Fatal(err)
	}
	ctx := p.Context()
	if ctx.ID() != "acme-crm" || ctx.StartURL() != "https://app.acme.com/" || len(ctx.Domains()) != 2 {
		t.Errorf("ctx basics wrong")
	}
	if s, err := ctx.Script("parse.js"); err != nil || !strings.Contains(s, "querySelectorAll") {
		t.Errorf("Script: %v", err)
	}
	if _, err := ctx.Script("../../etc/passwd"); err == nil {
		t.Error("script traversal allowed")
	}
	if f, err := ctx.Fragment("dismiss"); err != nil || len(f.Steps) != 1 {
		t.Errorf("Fragment: %v", err)
	}
	if e, ok := ctx.Selector("deal.row"); !ok || e.Candidates[0].XPath != "//tr[1]" {
		t.Errorf("Selector: %+v", e)
	}
	if def, pc, err := ctx.ResolveAction("create_contact"); err != nil || def.ActionType != "create_contact" || pc.ID() != "acme-crm" {
		t.Errorf("ResolveAction local: %v", err)
	}

	// Overlay wins over the package selector and leaves package files alone.
	healed := action.SelectorEntry{Candidates: []action.SelectorCandidate{{CSS: "tr.deal"}}}
	if err := r.WriteOverlaySelector("acme-crm", "deal.row", healed); err != nil {
		t.Fatal(err)
	}
	p, _ = r.Get("acme-crm")
	if e, _ := p.Context().Selector("deal.row"); e.Candidates[0].CSS != "tr.deal" {
		t.Errorf("overlay not applied: %+v", e)
	}
	if info, _ := r.Info("acme-crm"); info.Modified {
		t.Error("overlay counted as modification")
	}

	// Cross-package call_action goes through the registry.
	r.Seed(seedFS("1.0.0", "x"))
	if def, pc, err := ctx.ResolveAction("demo.hello"); err != nil || def.ActionType != "hello" || pc.ID() != "demo" {
		t.Errorf("cross ResolveAction: %v", err)
	}
	r.SetEnabled("demo", false)
	if _, _, err := ctx.ResolveAction("demo.hello"); err == nil {
		t.Error("disabled package resolvable")
	}
}

func TestInstallRejectsInvalid(t *testing.T) {
	r := newReg(t)
	files := acmeFiles()
	files["actions/list_deals.json"] = `{"actionType":"list_deals","sideEffects":"read","steps":[{"id":"x","type":"teleport"}]}`
	res, err := r.Install(writeTree(t, t.TempDir(), files), InstallOptions{})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if res == nil || res.Installed || !HasErrors(res.Issues) {
		t.Fatalf("result: %+v", res)
	}
	if _, err := r.Info("acme-crm"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("invalid package installed: %v", err)
	}
}

func TestInstallPolicyBlockedInstallsDisabled(t *testing.T) {
	r := newReg(t)
	files := acmeFiles()
	files["automation.json"] = strings.NewReplacer(`"app.acme.com", "*.acme.com"`, `"www.linkedin.com"`,
		"https://app.acme.com/", "https://www.linkedin.com/").Replace(files["automation.json"])
	files["actions/list_deals.json"] = strings.Replace(files["actions/list_deals.json"], "https://app.acme.com/deals", "https://www.linkedin.com/feed", 1)
	files["actions/create_contact.json"] = strings.Replace(files["actions/create_contact.json"], "https://app.acme.com/contacts/new", "https://www.linkedin.com/x", 1)
	res, err := r.Install(writeTree(t, t.TempDir(), files), InstallOptions{})
	if err != nil {
		t.Fatalf("Install: %v %+v", err, res.Issues)
	}
	if res.Review.PolicyBlocked == socialBuild() {
		t.Fatalf("PolicyBlocked=%v with social build=%v", res.Review.PolicyBlocked, socialBuild())
	}
	info, _ := r.Info("acme-crm")
	if info.Enabled == !socialBuild() {
		t.Errorf("enabled=%v in social build=%v", info.Enabled, socialBuild())
	}
	if !socialBuild() {
		if err := r.SetEnabled("acme-crm", true); err == nil {
			t.Error("enabling a policy-blocked package succeeded")
		}
	}
}

func TestPolicyAllows(t *testing.T) {
	m := Manifest{ID: "x", Site: Site{Domains: []string{"*.tiktok.com"}}}
	ok, reason := PolicyAllows(m)
	if ok != socialBuild() || (!ok && reason == "") {
		t.Errorf("tiktok: ok=%v reason=%q", ok, reason)
	}
	if ok, _ := PolicyAllows(Manifest{ID: "x", Site: Site{Domains: []string{"notlinkedin.com"}}}); !ok {
		t.Error("notlinkedin.com treated as social")
	}
	if ok, _ := PolicyAllows(Manifest{ID: "x", Policy: Policy{Tier: "social"}}); ok != socialBuild() {
		t.Error("tier social not gated")
	}
}

func TestDryRunAndUpdateChanges(t *testing.T) {
	r := newReg(t)
	if _, err := r.Install(acmeDir(t), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	files := acmeFiles()
	files["automation.json"] = strings.NewReplacer(`"1.2.0"`, `"1.3.0"`,
		`"*.acme.com"]`, `"*.acme.com", "cdn.acme.io"]`,
		`"extract_*"]`, `"extract_*", "download"]`,
		`["parse.js"]`, `["parse.js", "more.js"]`).Replace(files["automation.json"])
	files["scripts/parse.js"] = "return 2;\n"
	files["scripts/more.js"] = "return 3;\n"
	dir := writeTree(t, t.TempDir(), files)

	res, err := r.Install(dir, InstallOptions{DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v %+v", err, errorsOnly(res.Issues))
	}
	ch := res.Review.Changes
	if !res.DryRun || res.Installed || ch == nil || res.PreviousVersion != "1.2.0" {
		t.Fatalf("dry run result: %+v", res)
	}
	if strings.Join(ch.AddedDomains, ",") != "cdn.acme.io" || strings.Join(ch.AddedSteps, ",") != "download" ||
		strings.Join(ch.AddedScripts, ",") != "more.js" || strings.Join(ch.ChangedScripts, ",") != "parse.js" {
		t.Errorf("changes: %+v", ch)
	}
	if info, _ := r.Info("acme-crm"); info.Version != "1.2.0" {
		t.Error("dry run wrote")
	}
	if _, err := r.Install(dir, InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	info, _ := r.Info("acme-crm")
	if info.Version != "1.3.0" || info.PreviousVersion != "1.2.0" {
		t.Fatalf("update: %+v", info)
	}
	// Only current + previous are kept.
	files["automation.json"] = strings.Replace(files["automation.json"], `"1.3.0"`, `"1.4.0"`, 1)
	r.Install(writeTree(t, t.TempDir(), files), InstallOptions{})
	if _, err := os.Stat(filepath.Join(r.Root(), "acme-crm", "1.2.0")); !os.IsNotExist(err) {
		t.Error("third-oldest version not pruned")
	}
	if err := r.Rollback("acme-crm"); err != nil {
		t.Fatal(err)
	}
	if info, _ := r.Info("acme-crm"); info.Version != "1.3.0" {
		t.Errorf("rollback: %+v", info)
	}
	if err := r.Uninstall("acme-crm"); err != nil {
		t.Fatal(err)
	}
	if l, _ := r.List(true); len(l) != 0 {
		t.Errorf("imported package left in index after uninstall: %+v", l)
	}
}

// Round trip: seed → Export → Install into a second registry → Export is
// byte-identical, for every built-in and for an imported package.
func TestExportRoundTrip(t *testing.T) {
	a := newReg(t)
	if err := a.Seed(automations.Tree()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Install(acmeDir(t), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	b := newReg(t)
	infos, _ := a.List(false)
	for _, info := range infos {
		var first bytes.Buffer
		if err := a.Export(info.ID, &first, ExportOptions{}); err != nil {
			t.Fatalf("export %s: %v", info.ID, err)
		}
		f := filepath.Join(t.TempDir(), info.ID+".mpkg")
		os.WriteFile(f, first.Bytes(), 0o644)
		// Built-ins keep their source on the new machine only via seeding;
		// installing their export as builtin exercises the relaxed rules.
		src := SourceImported
		if info.Source == SourceBuiltin {
			src = SourceBuiltin
		}
		if res, err := b.install(f, InstallOptions{Source: src}, true); err != nil {
			t.Fatalf("install %s: %v %+v", info.ID, err, errorsOnly(res.Issues))
		}
		var second bytes.Buffer
		if err := b.Export(info.ID, &second, ExportOptions{}); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first.Bytes(), second.Bytes()) {
			t.Errorf("%s: export → install → export differs", info.ID)
		}
	}
}

func TestExportRecordingsAndSubset(t *testing.T) {
	r := newReg(t)
	r.Install(acmeDir(t), InstallOptions{})
	p, _ := r.Get("acme-crm")

	files, err := exportFiles(p, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["recordings/rec-1/meta.json"]; ok {
		t.Error("recordings exported by default")
	}
	files, _ = exportFiles(p, ExportOptions{WithRecordings: true})
	if _, ok := files["recordings/rec-1/meta.json"]; !ok {
		t.Error("WithRecordings ignored")
	}

	var buf bytes.Buffer
	if err := r.Export("acme-crm", &buf, ExportOptions{Actions: []string{"list_deals"}}); err != nil {
		t.Fatal(err)
	}
	fsys, err := readZip(&buf)
	if err != nil {
		t.Fatal(err)
	}
	sub, _ := OpenFS(fsys, SourceImported)
	if strings.Join(sub.Manifest.Actions, ",") != "list_deals" {
		t.Errorf("subset actions %v", sub.Manifest.Actions)
	}
	sel, _ := sub.Selectors()
	if _, ok := sel["contact.email"]; ok || len(sel) != 2 {
		t.Errorf("subset selectors %v", sel)
	}
	if _, err := sub.Fragment("dismiss"); err != nil {
		t.Error("fragment closure missing")
	}
	if _, err := sub.Action("create_contact"); err == nil {
		t.Error("unrelated action exported")
	}
	if is := errorsOnly(Validate(sub)); len(is) != 0 {
		t.Errorf("subset package invalid: %+v", is)
	}
	if err := r.Export("acme-crm", &buf, ExportOptions{Actions: []string{"nope"}}); err == nil {
		t.Error("unknown action exported")
	}
}

func TestAddAction(t *testing.T) {
	r := newReg(t)
	src, _ := OpenDir(acmeDir(t))

	// Not installed: creates a local package from the action's closure.
	res, err := r.AddAction("acme-crm", src, "create_contact", InstallOptions{})
	if err != nil {
		t.Fatalf("AddAction new: %v %+v", err, res)
	}
	p, _ := r.Get("acme-crm")
	if p.Source != SourceLocal || strings.Join(p.Manifest.Actions, ",") != "create_contact" {
		t.Fatalf("new package: %s %v", p.Source, p.Manifest.Actions)
	}

	// Installed: merges and bumps the patch version.
	res, err = r.AddAction("acme-crm", src, "list_deals", InstallOptions{})
	if err != nil {
		t.Fatalf("AddAction merge: %v %+v", err, errorsOnly(res.Issues))
	}
	p, _ = r.Get("acme-crm")
	if p.Manifest.Version != "1.2.1" || len(p.Manifest.Actions) != 2 {
		t.Fatalf("merged: %s %v", p.Manifest.Version, p.Manifest.Actions)
	}
	sel, _ := p.Selectors()
	if len(sel) != 4 {
		t.Errorf("merged selectors: %v", sel)
	}
	if res.Review.Changes == nil || strings.Join(res.Review.Changes.AddedScripts, ",") != "parse.js" {
		t.Errorf("merge changes: %+v", res.Review.Changes)
	}
}

func TestInstallFromURL(t *testing.T) {
	pkg, _ := os.ReadFile(packDir(t, acmeDir(t)))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/missing":
			http.NotFound(w, req)
		case "/downgrade":
			http.Redirect(w, req, "http://"+req.Host+"/acme.mpkg", http.StatusFound)
		default:
			w.Write(pkg)
		}
	}))
	defer srv.Close()
	oldTransport := httpClient.Transport
	httpClient.Transport = srv.Client().Transport
	defer func() { httpClient.Transport = oldTransport }()

	r := newReg(t)
	res, err := r.Install(srv.URL+"/acme.mpkg", InstallOptions{DryRun: true})
	if err != nil || res.SHA256 != sha256Hex(pkg) {
		t.Fatalf("dry run: %v sha=%q", err, res.SHA256)
	}
	if res, err := r.Install(srv.URL+"/acme.mpkg", InstallOptions{ExpectSHA256: res.SHA256}); err != nil || !res.Installed {
		t.Fatalf("pinned URL install: %v %+v", err, res)
	}
	if _, err := r.Install(srv.URL+"/acme.mpkg", InstallOptions{ExpectSHA256: strings.Repeat("0", 64)}); !errors.Is(err, ErrSHA256Mismatch) {
		t.Errorf("wrong pin: %v", err)
	}
	if _, err := r.Install(srv.URL+"/missing", InstallOptions{}); err == nil {
		t.Error("404 accepted")
	}
	if _, err := r.Install(srv.URL+"/downgrade", InstallOptions{}); err == nil || !strings.Contains(err.Error(), "non-https") {
		t.Errorf("redirect to http accepted: %v", err)
	}
	if _, err := r.Install(strings.Replace(srv.URL, "https://", "http://", 1)+"/acme.mpkg", InstallOptions{}); err == nil || !strings.Contains(err.Error(), "plain http") {
		t.Errorf("http URL accepted: %v", err)
	}
	old := MaxArchiveBytes
	MaxArchiveBytes = 100
	defer func() { MaxArchiveBytes = old }()
	if _, err := r.Install(srv.URL+"/acme.mpkg", InstallOptions{}); !errors.Is(err, ErrUnsafeArchive) {
		t.Errorf("oversized download: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(r.Root(), ".downloads")); len(entries) != 0 {
		t.Errorf("download scratch left behind: %d files", len(entries))
	}
}

func TestInstallSHA256Pin(t *testing.T) {
	dir := acmeDir(t)
	file := packDir(t, dir)
	raw, _ := os.ReadFile(file)
	r := newReg(t)

	// .mpkg: the hash of the file bytes.
	res, err := r.Install(file, InstallOptions{DryRun: true})
	if err != nil || res.SHA256 != sha256Hex(raw) {
		t.Fatalf("file dry run: %v %q", err, res.SHA256)
	}
	// Directory: the hash of its deterministic pack (same bytes as Pack).
	res, err = r.Install(dir, InstallOptions{DryRun: true})
	if err != nil || res.SHA256 != sha256Hex(raw) {
		t.Fatalf("dir dry run: %v %q", err, res.SHA256)
	}
	pin := res.SHA256
	// The directory changes between review and install: refused.
	os.WriteFile(filepath.Join(dir, "scripts", "parse.js"), []byte("return 'evil';\n"), 0o644)
	if _, err := r.Install(dir, InstallOptions{ExpectSHA256: pin}); !errors.Is(err, ErrSHA256Mismatch) {
		t.Fatalf("changed dir installed: %v", err)
	}
	if _, err := r.Info("acme-crm"); !errors.Is(err, ErrNotInstalled) {
		t.Error("mismatch still wrote")
	}
	if res, err := r.Install(file, InstallOptions{ExpectSHA256: strings.ToUpper(pin)}); err != nil || !res.Installed {
		t.Fatalf("pinned file install: %v", err)
	}

	// AddAction pins against the OpenFile bytes.
	src, _ := OpenFile(file)
	if src.SHA256() != pin {
		t.Errorf("Package.SHA256 = %q", src.SHA256())
	}
	if _, err := newReg(t).AddAction("acme-crm", src, "list_deals", InstallOptions{ExpectSHA256: strings.Repeat("1", 64)}); !errors.Is(err, ErrSHA256Mismatch) {
		t.Errorf("AddAction wrong pin: %v", err)
	}
	if res, err := newReg(t).AddAction("acme-crm", src, "list_deals", InstallOptions{ExpectSHA256: pin}); err != nil || res.SHA256 != pin {
		t.Errorf("AddAction pinned: %v", err)
	}
}

func TestConcurrentWritersKeepIndexConsistent(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".monoagent")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _ := Open(home) // separate Registry values: only the flock serialises them
			if err := r.Seed(seedFS("1.0.0", "a")); err != nil {
				t.Error(err)
			}
			r.SetEnabled("demo", false)
			r.SetEnabled("demo", true)
		}()
	}
	wg.Wait()
	r, _ := Open(home)
	if info, err := r.Info("demo"); err != nil || !info.Enabled {
		t.Fatalf("after concurrent writes: %+v %v", info, err)
	}
}

func TestDefaultHonoursHOME(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	r, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if r.Root() != filepath.Join(home, ".monoagent", "automations") {
		t.Errorf("root %s", r.Root())
	}
}

func TestContextForm(t *testing.T) {
	p, err := OpenDir(acmeDir(t))
	if err != nil {
		t.Fatal(err)
	}
	f, ok := p.Context().(interface{ Form(string) ([]byte, error) })
	if !ok {
		t.Fatal("context has no Form method")
	}
	if b, err := f.Form("create_contact"); err != nil || string(b) != "{}\n" {
		t.Errorf("Form: %q %v", b, err)
	}
	if _, err := f.Form("list_deals"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing form: %v", err)
	}
	for _, bad := range []string{"../automation", "a/b", ".."} {
		if _, err := f.Form(bad); err == nil {
			t.Errorf("Form(%q) allowed", bad)
		}
	}
}
