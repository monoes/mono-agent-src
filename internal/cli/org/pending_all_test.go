package org

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/orgchat"
	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
	"github.com/monoes/mono-agent/internal/workflow"
)

// pendingOrg is an org whose roles work in its isolated workspace.
const pendingOrg = `{"name":"bud","goal":"g","run_config":{"workspace":"isolated"},"roles":[
	{"id":"lead","title":"Lead","type":"boss","reports_to":null},
	{"id":"writer","title":"Writer","type":"specialist","reports_to":"lead"}]}`

// pendingFake stands in for monomind's item lists: it answers by the folder
// and org it is asked about, and records what it was asked.
type pendingFake struct {
	orgchat.Client // only All is used; anything else panics

	mu       sync.Mutex
	lists    map[string]string // "<root>\x00<org>\x00<kind>" -> payload
	fail     map[string]string // "<root>\x00<org>" -> error text
	hang     map[string]bool   // "<root>\x00<org>": never answers, whatever the context says
	delay    time.Duration
	calls    []string
	active   map[string]int // org reads in flight
	maxOrgs  int
	release  chan struct{}
	finished sync.WaitGroup
}

func (f *pendingFake) All(ctx context.Context, root, org, kind string) (json.RawMessage, error) {
	key := root + "\x00" + org
	f.mu.Lock()
	f.calls = append(f.calls, key+"\x00"+kind)
	f.active[key]++
	f.maxOrgs = max(f.maxOrgs, len(f.active))
	hang, failure, payload := f.hang[key], f.fail[key], f.lists[key+"\x00"+kind]
	if hang {
		f.finished.Add(1) // under the lock: the cleanup takes it before it waits
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		if f.active[key]--; f.active[key] == 0 {
			delete(f.active, key)
		}
		f.mu.Unlock()
	}()
	if hang {
		defer f.finished.Done()
		<-f.release
		return nil, errors.New("released")
	}
	time.Sleep(f.delay)
	if failure != "" {
		return nil, errors.New(failure)
	}
	if payload == "" {
		payload = `{"v":1,"items":[]}`
	}
	return json.RawMessage(payload), nil
}

func (f *pendingFake) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func usePendingFake(t *testing.T) *pendingFake {
	t.Helper()
	f := &pendingFake{lists: map[string]string{}, fail: map[string]string{}, hang: map[string]bool{}, active: map[string]int{}, release: make(chan struct{})}
	prev := orgChatClient
	orgChatClient = f
	t.Cleanup(func() {
		close(f.release)
		f.mu.Lock()
		//lint:ignore SA2001 the empty section orders every Add before the Wait
		f.mu.Unlock()
		f.finished.Wait()
		orgChatClient = prev
	})
	return f
}

// twoProfileOrgs is a database whose profiles "default" (Default) and "p2"
// (Work) each have a folder of their own.
func twoProfileOrgs(t *testing.T) (cfg *testConfig, home, work string) {
	t.Helper()
	testhome.SetHome(t, t.TempDir())
	asOrgOperator(t)
	cfg = &testConfig{DBPath: filepath.Join(t.TempDir(), "o.db")}
	db, err := testDeps(&testConfig{DBPath: cfg.DBPath, ProfileID: "default"}).OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	home, work = t.TempDir(), t.TempDir()
	mustExec(t, db, `UPDATE profiles SET root_dir = ? WHERE id = 'default'`, home)
	mustExec(t, db, `INSERT INTO profiles (id, name, root_dir) VALUES ('p2', 'Work', ?)`, work)
	return cfg, home, work
}

func mustExec(t *testing.T, db *storage.Database, q string, args ...any) {
	t.Helper()
	if _, err := db.DB.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

type pendingAllDoc struct {
	V     int `json:"v"`
	Items []struct {
		ProfileID   string `json:"profile_id"`
		ProfileName string `json:"profile_name"`
		Org         string `json:"org"`
		Kind        string `json:"kind"`
		Ref         string `json:"ref"`
		Text        string `json:"text"`
		Command     string `json:"command"`
		Cwd         string `json:"cwd"`
		RequestID   string `json:"request_id"`
	} `json:"items"`
	Errors []pendingError `json:"errors"`
}

func operatorPendingDeps(cfg *testConfig) Deps {
	d := testDeps(cfg)
	d.RequireOperator = func(string) error { return nil }
	return d
}

func runPending(t *testing.T, d Deps, args ...string) (string, error) {
	t.Helper()
	cmd := NewCommand(d)
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var err error
	out := captureStdout(t, func() { err = cmd.Execute() })
	return out, err
}

func pendingAll(t *testing.T, cfg *testConfig) pendingAllDoc {
	t.Helper()
	out, err := runPending(t, operatorPendingDeps(cfg), "pending", "--all")
	if err != nil {
		t.Fatalf("pending --all: %v\n%s", err, out)
	}
	var doc pendingAllDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("pending --all: %v\n%s", err, out)
	}
	return doc
}

// `org pending --all` lists what the orgs of every profile wait on: each org
// is read in its own profile's folder, and each item names its profile.
func TestOrgPendingAllListsEveryProfile(t *testing.T) {
	cfg, home, work := twoProfileOrgs(t)
	fake := usePendingFake(t)
	// The same org name in both folders: nothing of one may show as the other's.
	writeOrgFile(t, home, "bud.json", pendingOrg)
	writeOrgFile(t, work, "bud.json", pendingOrg)
	fake.lists[home+"\x00bud\x00questions"] = `{"items":[{"questionId":"q-home","role":"lead","question":"Home?","ts":1,"answer":null},
		{"questionId":"q-old","role":"lead","question":"old","ts":0,"answer":"yes"}]}`
	fake.lists[work+"\x00bud\x00approvals"] = `{"items":[{"roleId":"writer","action":"Bash","question":"Approve?","ts":2,"approved":null,"requestId":"apr-work","input":{"command":"printf hi"}}]}`
	fake.lists[work+"\x00bud\x00gates"] = `{"items":[{"id":"gate-work","name":"publish","description":"Publish","roleId":"lead","status":"pending","createdAt":3}]}`

	doc := pendingAll(t, cfg)
	var got []string
	for _, it := range doc.Items {
		got = append(got, fmt.Sprintf("%s/%s/%s/%s:%s", it.ProfileID, it.ProfileName, it.Org, it.Kind, it.Ref))
	}
	slices.Sort(got)
	want := []string{"default/Default/bud/question:q-home", "p2/Work/bud/approval:apr-work", "p2/Work/bud/gate:gate-work"}
	if doc.V != 1 || len(doc.Errors) != 0 || !slices.Equal(got, want) {
		t.Fatalf("v %d, errors %+v, items %v; want %v", doc.V, doc.Errors, got, want)
	}
	for _, it := range doc.Items {
		if it.Ref != "apr-work" {
			continue
		}
		cwd := filepath.Join(work, ".monomind", "orgs", "bud", "workspace")
		if it.RequestID != "apr-work" || it.Command != "printf hi" || it.Cwd != cwd {
			t.Errorf("the approval = %+v (want folder %s)", it, cwd)
		}
	}
	for _, c := range fake.asked() {
		if !strings.HasPrefix(c, home+"\x00") && !strings.HasPrefix(c, work+"\x00") {
			t.Errorf("read outside the profiles' folders: %q", c)
		}
	}
	if n := len(fake.asked()); n != 6 {
		t.Errorf("%d reads, want 3 lists of each of the 2 orgs", n)
	}
}

// Without --all the document is as it was: no profile fields.
func TestOrgPendingOfOneProfileNamesNoProfile(t *testing.T) {
	cfg, home, work := twoProfileOrgs(t)
	fake := usePendingFake(t)
	writeOrgFile(t, home, "bud.json", pendingOrg)
	writeOrgFile(t, work, "other.json", pendingOrg)
	fake.lists[home+"\x00bud\x00questions"] = `{"items":[{"questionId":"q-home","role":"lead","question":"Home?","ts":1,"answer":null}]}`
	cfg.ProfileID = "default"
	out, err := runPending(t, testDeps(cfg), "pending")
	if err != nil || !strings.Contains(out, "q-home") || strings.Contains(out, "profile") || strings.Contains(out, "other") {
		t.Fatalf("pending: %v\n%s", err, out)
	}
}

// One org or one profile that cannot be read is listed under errors, with its
// profile, and the others still show.
func TestOrgPendingAllReportsWhatItCannotRead(t *testing.T) {
	cfg, home, work := twoProfileOrgs(t)
	fake := usePendingFake(t)
	writeOrgFile(t, home, "bud.json", pendingOrg)
	writeOrgFile(t, work, "broken.json", pendingOrg)
	writeOrgFile(t, work, "fine.json", pendingOrg)
	fake.lists[home+"\x00bud\x00questions"] = `{"items":[{"questionId":"q-home","role":"lead","question":"Home?","ts":1,"answer":null}]}`
	fake.fail[work+"\x00broken"] = "state file is not valid JSON"
	fake.lists[work+"\x00fine\x00gates"] = `{"items":[{"id":"gate-fine","name":"g","description":"d","roleId":"lead","status":"pending","createdAt":3}]}`
	// A third profile whose orgs folder is a file.
	bad := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bad, ".monomind"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, ".monomind", "orgs"), []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO profiles (id, name, root_dir) VALUES ('p3', 'Broken', ?)`, bad)
	db.Close()

	doc := pendingAll(t, cfg)
	var refs []string
	for _, it := range doc.Items {
		refs = append(refs, it.Ref)
	}
	slices.Sort(refs)
	if !slices.Equal(refs, []string{"gate-fine", "q-home"}) {
		t.Fatalf("items = %v", refs)
	}
	byOrg := map[string]int{}
	for _, e := range doc.Errors {
		if e.Error == "" {
			t.Errorf("an empty error: %+v", e)
		}
		byOrg[e.ProfileID+"/"+e.ProfileName+"/"+e.Org]++
	}
	// Three lists of the broken org, and one entry for the profile (no org).
	if len(byOrg) != 2 || byOrg["p2/Work/broken"] != 3 || byOrg["p3/Broken/"] != 1 {
		t.Fatalf("errors = %v (%+v)", byOrg, doc.Errors)
	}
}

// Orgs are read a few at a time, and one that never answers, whatever its
// context says, costs its own time limit only.
func TestOrgPendingAllIsBounded(t *testing.T) {
	cfg, home, work := twoProfileOrgs(t)
	fake := usePendingFake(t)
	fake.delay = 20 * time.Millisecond
	for i := range 5 {
		writeOrgFile(t, home, fmt.Sprintf("h%d.json", i), pendingOrg)
		writeOrgFile(t, work, fmt.Sprintf("w%d.json", i), pendingOrg)
	}
	fake.hang[home+"\x00h0"] = true
	fake.lists[work+"\x00w4\x00gates"] = `{"items":[{"id":"gate-w4","name":"g","description":"d","roleId":"lead","status":"pending","createdAt":3}]}`
	shortPendingLimits(t, 2*time.Second, time.Minute) // room for a loaded CI runner

	doc := pendingAll(t, cfg)
	if len(doc.Items) != 1 || doc.Items[0].Ref != "gate-w4" {
		t.Fatalf("items = %+v", doc.Items)
	}
	if len(doc.Errors) != 1 || doc.Errors[0].Org != "h0" || doc.Errors[0].ProfileID != "default" || !strings.Contains(doc.Errors[0].Error, "timed out") {
		t.Fatalf("errors = %+v", doc.Errors)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	// The abandoned org still counts in the fake, so one more than the bound may be seen.
	if fake.maxOrgs > pendingParallel+1 || fake.maxOrgs < 2 {
		t.Fatalf("%d orgs read at once, want at most %d", fake.maxOrgs, pendingParallel)
	}
}

// The whole listing has a time limit too: orgs it did not get to are named.
func TestOrgPendingAllStopsAtItsOverallLimit(t *testing.T) {
	cfg, home, _ := twoProfileOrgs(t)
	fake := usePendingFake(t)
	for i := range 12 {
		name := fmt.Sprintf("h%02d", i)
		writeOrgFile(t, home, name+".json", pendingOrg)
		fake.hang[home+"\x00"+name] = true
	}
	shortPendingLimits(t, time.Minute, 200*time.Millisecond)

	start := time.Now()
	doc := pendingAll(t, cfg)
	if took := time.Since(start); took > 20*time.Second {
		t.Fatalf("took %v", took)
	}
	named := map[string]bool{}
	for _, e := range doc.Errors {
		named[e.Org] = true
	}
	if len(doc.Items) != 0 || len(doc.Errors) != 12 || len(named) != 12 {
		t.Fatalf("items %d, errors %+v", len(doc.Items), doc.Errors)
	}
	if n := len(fake.asked()); n > 3*pendingParallel {
		t.Fatalf("%d reads started after the limit", n)
	}
}

func shortPendingLimits(t *testing.T, perOrg, overall time.Duration) {
	t.Helper()
	o, a, g := pendingAllOrgTimeout, pendingAllTimeout, pendingAllGrace
	pendingAllOrgTimeout, pendingAllTimeout, pendingAllGrace = perOrg, overall, 100*time.Millisecond
	t.Cleanup(func() { pendingAllOrgTimeout, pendingAllTimeout, pendingAllGrace = o, a, g })
}

// Crossing profiles is the user's alone: an agent is refused before anything is
// read, whichever check finds it, and an unwired check refuses too.
func TestOrgPendingAllRefusesAnAgent(t *testing.T) {
	cfg, home, _ := twoProfileOrgs(t)
	fake := usePendingFake(t)
	writeOrgFile(t, home, "bud.json", pendingOrg)
	refused := func(what string, d Deps) {
		t.Helper()
		d.OpenDB = func() (*storage.Database, error) {
			t.Errorf("%s: the database was opened", what)
			return nil, errors.New("no")
		}
		out, err := runPending(t, d, "pending", "--all")
		if err == nil || strings.Contains(out, "bud") {
			t.Fatalf("%s: allowed: %v\n%s", what, err, out)
		}
	}
	refused("no operator check", testDeps(cfg))
	denied := testDeps(cfg)
	denied.RequireOperator = func(string) error { return errors.New("not the operator") }
	refused("the operator check", denied)
	markers := append(orgsign.AgentContextMarkers(), hilsend.EnvAgentContext, "MONOAGENT_ACTOR", workflow.EnvWorkflowRun, browserctl.EnvAICaller)
	for _, marker := range markers {
		t.Setenv(marker, "agent")
		refused(marker, operatorPendingDeps(cfg))
		t.Setenv(marker, "")
	}
	opgate.SetForTest(t, func(bool) string { return turnStandIn })
	refused("a coder turn", operatorPendingDeps(cfg))
	if calls := fake.asked(); len(calls) != 0 {
		t.Fatalf("a refused caller reached monomind: %q", calls)
	}
}

// --all contradicts a named profile or project folder, and --all=false is the
// one-profile command, which needs its profile's folder.
func TestOrgPendingAllTakesNoProfileOrProject(t *testing.T) {
	cfg, home, _ := twoProfileOrgs(t)
	fake := usePendingFake(t)
	writeOrgFile(t, home, "bud.json", pendingOrg)
	named := &testConfig{DBPath: cfg.DBPath, ProfileID: "p2"}
	for what, run := range map[string]func() (string, error){
		"--profile": func() (string, error) { return runPending(t, operatorPendingDeps(named), "pending", "--all") },
		"--project": func() (string, error) {
			return runPending(t, operatorPendingDeps(cfg), "--project", home, "pending", "--all")
		},
	} {
		out, err := run()
		if exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "leave out") && !strings.Contains(err.Error(), "drop") {
			t.Errorf("%s: %v\n%s", what, err, out)
		}
	}
	if calls := fake.asked(); len(calls) != 0 {
		t.Fatalf("a refused call reached monomind: %q", calls)
	}
	d := operatorPendingDeps(cfg)
	d.OpenDB = func() (*storage.Database, error) { return nil, errors.New("no database here") }
	if out, err := runPending(t, d, "pending", "--all=false"); err == nil || !strings.Contains(err.Error(), "no database here") {
		t.Fatalf("--all=false must look its profile's folder up first: %v\n%s", err, out)
	}
}
