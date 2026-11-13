package health

import (
	"context"
	"strings"
	"testing"
	"time"
)

func readyFacts() OpenModeFacts {
	return OpenModeFacts{GOOS: "linux", BindingOn: true, Steps: []OpenModeStep{
		{Name: "cgroup v2 unified hierarchy"}, {Name: "systemd-run available"}}}
}

func openModeEnv(f OpenModeFacts, vault string) *Env {
	return &Env{ProfileID: "P",
		OpenMode:      func(context.Context) OpenModeFacts { return f },
		VaultState:    func(context.Context, string) (string, error) { return vault, nil },
		MonoesAccount: func(context.Context) AccountInfo { return AccountInfo{State: "ok", Email: "a@b.c"} }}
}

func rowOf(t *testing.T, res Result, id string) Result {
	t.Helper()
	for _, c := range res.Children {
		if c.ID == CheckOpenMode+"."+id {
			return c
		}
	}
	t.Fatalf("no row %s in %+v", id, res.Children)
	return Result{}
}

// Every prerequisite on its own: the row's status, what it says, and the exact fix it names.
func TestOpenModeRows(t *testing.T) {
	cases := []struct {
		name   string
		mut    func(*OpenModeFacts)
		vault  string
		row    string
		status Status
		text   string // in the summary
		fix    string // in the detail
	}{
		{"all met", func(*OpenModeFacts) {}, "ok", "binding", StatusOK, "on", ""},
		{"binding off", func(f *OpenModeFacts) { f.BindingOn = false }, "ok", "binding", StatusWarn, "off (switched off)", "coder set --peercred-binding on"},
		{"binding cannot run, default off", func(f *OpenModeFacts) { f.BindingOn, f.BindingUnbound = false, "old kernel" }, "ok", "binding", StatusWarn, "open-ended runs here would start without peer-credential binding: old kernel", ""},
		{"explicit on that cannot run", func(f *OpenModeFacts) { f.BindingExplicitOnBroken = "old kernel" }, "ok", "binding", StatusWarn, "peer-credential binding is switched on but cannot run: old kernel", "coder set --peercred-binding off"},
		{"binding unreadable", func(f *OpenModeFacts) { f.BindingErr = "locked" }, "ok", "binding", StatusWarn, "cannot read", ""},
		{"macOS", func(f *OpenModeFacts) { f.GOOS, f.Unsupported = "darwin", "not available on macOS yet: x" }, "ok", "os", StatusWarn, "macOS", "#1610"},
		{"kernel cannot bind", func(f *OpenModeFacts) { f.BindingReadyErr = "needs Linux 6.5 or later" }, "ok", "binding_service", StatusWarn, "6.5", "Linux 6.5"},
		{"no delegation", func(f *OpenModeFacts) {
			f.Steps[0] = OpenModeStep{Name: "cgroup v2 unified hierarchy", Err: "no", Fix: "boot with X"}
		}, "ok", "containment_cgroup_v2_unified_hierarchy", StatusWarn, "no", "boot with X"},
		{"no systemd-run", func(f *OpenModeFacts) {
			f.Steps[1] = OpenModeStep{Name: "systemd-run available", Err: "missing", Fix: "install systemd"}
		}, "ok", "containment_systemd-run_available", StatusWarn, "missing", "install systemd"},
		{"no vault key yet", func(*OpenModeFacts) {}, "uninitialized", "vault", StatusWarn, "no vault key", "secret add --kind secret"},
		{"vault key lost", func(*OpenModeFacts) {}, "key-missing", "vault", StatusWarn, "missing", ""},
		{"host running", func(f *OpenModeFacts) { f.HostRunning = true }, "ok", "host", StatusOK, "running", ""},
		{"host not running", func(*OpenModeFacts) {}, "ok", "host", StatusInfo, "not running", ""},
		{"active run", func(f *OpenModeFacts) { f.ActiveRun = true }, "ok", "active_run", StatusWarn, "already active", "doomsday stop"},
		{"run table unreadable", func(f *OpenModeFacts) { f.ActiveErr = "busy" }, "ok", "active_run", StatusWarn, "cannot read", ""},
	}
	for _, tc := range cases {
		f := readyFacts()
		f.Steps = append([]OpenModeStep(nil), f.Steps...)
		tc.mut(&f)
		res := checkOpenMode(context.Background(), openModeEnv(f, tc.vault))
		r := rowOf(t, res, tc.row)
		if r.Status != tc.status || !strings.Contains(r.Summary, tc.text) || !strings.Contains(r.Detail, tc.fix) {
			t.Errorf("%s: %q %q / %q, want %q containing %q and detail %q", tc.name, r.Status, r.Summary, r.Detail, tc.status, tc.text, tc.fix)
		}
	}
}

// Signed out is a fail only once enforcement is on, and names the login command.
func TestOpenModeAccountRow(t *testing.T) {
	env := openModeEnv(readyFacts(), "ok")
	env.MonoesAccount = func(context.Context) AccountInfo {
		return AccountInfo{State: "locked", Reason: "not_logged_in", Enforced: true, EnforceFrom: time.Now().Add(-time.Hour)}
	}
	r := rowOf(t, checkOpenMode(context.Background(), env), "account")
	if r.Status != StatusWarn || r.FixID != FixMonoesLogin {
		t.Errorf("%q fix %q", r.Status, r.FixID)
	}
}

// The parent is ok only when every prerequisite is met, and counts what is not.
func TestOpenModeParent(t *testing.T) {
	res := checkOpenMode(context.Background(), openModeEnv(readyFacts(), "ok"))
	if res.Status != StatusOK || res.Summary != "can run on this machine" {
		t.Errorf("ready: %q %q", res.Status, res.Summary)
	}
	f := readyFacts()
	f.BindingOn, f.BindingReadyErr = false, "no"
	res = checkOpenMode(context.Background(), openModeEnv(f, "ok"))
	if res.Status != StatusWarn || !strings.Contains(res.Summary, "2 of") {
		t.Errorf("two problems: %q %q", res.Status, res.Summary)
	}
}

func TestOpenModeSkips(t *testing.T) {
	if r := checkOpenMode(context.Background(), &Env{ProfileID: "P"}); r.Status != StatusSkip {
		t.Errorf("no hook: %q", r.Status)
	}
	env := openModeEnv(readyFacts(), "ok")
	env.ProfileID = ""
	if r := checkOpenMode(context.Background(), env); r.Status != StatusSkip {
		t.Errorf("no profile: %q", r.Status)
	}
}

// The registered row is not required (exit code 0 whatever it finds), is in the core group, and offers no Fix object
// (`doctor --fix` and setup list fixes): the binding advice is the command in the detail.
func TestOpenModeRegisteredNotRequired(t *testing.T) {
	f := readyFacts()
	f.BindingOn = false
	rep := Default().Run(context.Background(), openModeEnv(f, "ok"), Options{IDs: []string{CheckOpenMode}})
	var parent, bind *Result
	for i := range rep.Results {
		switch rep.Results[i].ID {
		case CheckOpenMode:
			parent = &rep.Results[i]
		case CheckOpenMode + ".binding":
			bind = &rep.Results[i]
		}
	}
	if parent == nil || bind == nil {
		t.Fatalf("rows missing: %+v", rep.Results)
	}
	if parent.Required || parent.Group != GroupCore || parent.Title != "Open-ended mode" {
		t.Errorf("parent %+v", parent)
	}
	if bind.Fix != nil || !strings.Contains(bind.Detail, "monoagentcli --profile P coder set --peercred-binding on") {
		t.Errorf("binding fix %+v detail %q", bind.Fix, bind.Detail)
	}
}

// On a host that lacks everything (no delegation, no systemd-run, old kernel, no key, locked login) no row fails, so
// `setup --group core` and the CI doctor smoke test (no failing core row) stay green; the fixes are still named.
func TestOpenModeNeverFailsTheCoreGroup(t *testing.T) {
	f := OpenModeFacts{GOOS: "linux", BindingReadyErr: "old kernel", Unsupported: "macOS", ActiveRun: true,
		Steps: []OpenModeStep{{Name: "cgroup v2 delegation to the user", Err: "no delegation", Fix: "log in through a desktop session"}}}
	env := openModeEnv(f, "key-missing")
	env.MonoesAccount = func(context.Context) AccountInfo {
		return AccountInfo{State: "locked", Reason: "not_logged_in", Enforced: true, EnforceFrom: time.Now().Add(-time.Hour)}
	}
	rep := Default().Run(context.Background(), env, Options{Groups: []string{GroupCore}, IDs: []string{CheckOpenMode}})
	n := 0
	for _, r := range rep.Results {
		if !strings.HasPrefix(r.ID, CheckOpenMode) {
			continue
		}
		n++
		if r.Status == StatusFail || r.Group != GroupCore || r.Required {
			t.Errorf("%s: %q group %q required %v", r.ID, r.Status, r.Group, r.Required)
		}
	}
	if n < 8 {
		t.Errorf("only %d rows", n)
	}
	d := rowOf(t, checkOpenMode(context.Background(), env), "containment_cgroup_v2_delegation_to_the_user")
	if d.Status != StatusWarn || !strings.Contains(d.Detail, "log in through a desktop session") {
		t.Errorf("%+v", d)
	}
}
