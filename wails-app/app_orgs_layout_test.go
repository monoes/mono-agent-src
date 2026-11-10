package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orgsign"
)

// layoutOrg is a sections org in monomind's own key order (requires before
// roles, run_config unsorted) — the layout every app save must keep.
const layoutOrg = `{
  "name": "sec-org",
  "goal": "ship <fast> & safe",
  "requires": {
    "sections": 1
  },
  "status": "stopped",
  "schedule": null,
  "run_config": {
    "max_concurrent_agents": 20,
    "idle_minutes": 0,
    "budget_usd": 30.0,
    "completion": {"mode": "dag", "protocol": "sections-v1"}
  },
  "roles": [
    {
      "id": "boss",
      "title": "Boss",
      "type": "boss",
      "reports_to": null,
      "responsibilities": ["Lead the org."],
      "policy": {"sandbox": {"mode": "off"}}
    },
    {
      "id": "dev-lead",
      "title": "Dev Lead",
      "type": "specialist",
      "reports_to": "boss",
      "responsibilities": ["Lead development."],
      "policy": {"sandbox": {"mode": "off"}}
    },
    {
      "id": "coder",
      "title": "Coder",
      "type": "specialist",
      "reports_to": "dev-lead",
      "responsibilities": ["Write code."],
      "policy": {"sandbox": {"mode": "off"}}
    }
  ],
  "sections": {
    "development": {
      "members": ["dev-lead", "coder"],
      "lead": "dev-lead",
      "publishes": ["build"],
      "budget": {"usd": 6.5}
    }
  },
  "documents": {
    "build": {
      "schema": {"type": "object"}
    }
  }
}
`

// echoOrgCLI is a stub monoagentcli whose `org reconcile-doc` answers with
// the document it was sent (as the real one does when no row changes it),
// decoded and re-encoded by the caller like any CLI answer; validate says
// valid unless invalid is set.
func echoOrgCLI(t *testing.T, invalid bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub CLI is a shell script (unix-only)")
	}
	valid := `{"v":1,"org":"sec-org","valid":true}`
	if invalid {
		valid = `{"v":1,"org":"sec-org","valid":false,"error":"nope"}`
	}
	script := "#!/bin/sh\ncase \"$*\" in\n" +
		"  *reconcile-doc*) body=$(cat); printf '{\"v\":1,\"org\":%s,\"reconcile\":[]}\\n' \"$body\" ;;\n" +
		"  *validate*) printf '%s\\n' '" + valid + "' ;;\n" +
		"  *) printf '{\"ok\":true}\\n' ;;\nesac\n"
	bin := filepath.Join(t.TempDir(), fakeCLIName)
	if err := writeFakeCLI(bin, script); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", bin)
}

func seedLayoutOrg(t *testing.T, root string) string {
	t.Helper()
	path, err := orgdesign.ConfigPath(root, "sec-org")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(layoutOrg), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestAppSavesKeepTheLoadedLayout: an app edit rewrites only the edited
// line, through the full saveOrgDoc path (write, CLI validate, reconcile,
// write again) — the second write used to lose the layout.
func TestAppSavesKeepTheLoadedLayout(t *testing.T) {
	cases := []struct {
		name string
		edit func(d *orgdesign.Doc) error
		from string
		to   string
	}{
		{"UpdateOrgRole", func(d *orgdesign.Doc) error {
			title := "Principal Coder"
			_, err := d.UpdateRole("coder", orgdesign.RolePatch{Title: &title})
			return err
		},
			`"title": "Coder"`, `"title": "Principal Coder"`},
		{"SetOrgRoleReportsTo", func(d *orgdesign.Doc) error { return d.SetReportsTo("coder", "boss") },
			`"reports_to": "dev-lead",
      "responsibilities": ["Write code."]`, `"reports_to": "boss",
      "responsibilities": ["Write code."]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			echoOrgCLI(t, false)
			a, root := newOrgDesignApp(t)
			path := seedLayoutOrg(t, root)
			// What the bindings (UpdateOrgRole, SetOrgRoleReportsTo, ...) do,
			// minus the Wails event they emit afterwards.
			d, err := orgdesign.Load(root, "sec-org")
			if err != nil {
				t.Fatal(err)
			}
			if err := c.edit(d); err != nil {
				t.Fatal(err)
			}
			if _, err := a.saveOrgDoc(root, d, false); err != nil {
				t.Fatalf("save failed: %v", err)
			}
			want := strings.Replace(layoutOrg, c.from, c.to, 1)
			if got := readFile(t, path); got != want {
				t.Fatalf("app save reordered the file:\n--- want\n%s\n--- got\n%s", want, got)
			}
		})
	}
}

// TestAppSaveKeepsLoadedState: after the save the Doc still describes the
// file on disk, so the re-sign rule (loaded sha == bytes on disk) holds for
// whatever the caller does next.
func TestAppSaveKeepsLoadedState(t *testing.T) {
	echoOrgCLI(t, false)
	a, root := newOrgDesignApp(t)
	path := seedLayoutOrg(t, root)
	d, err := orgdesign.Load(root, "sec-org")
	if err != nil {
		t.Fatal(err)
	}
	d.Goal = "edited"
	sha, err := a.saveOrgDoc(root, d, false)
	if err != nil {
		t.Fatal(err)
	}
	if on := orgsign.SHA256([]byte(readFile(t, path))); sha != on || d.LoadedSHA() != on {
		t.Fatalf("returned sha %s, Doc.LoadedSHA %s, file sha %s", sha, d.LoadedSHA(), on)
	}
	// A second edit through the same Doc still follows the layout.
	d.Goal = "edited again"
	if _, err := a.saveOrgDoc(root, d, false); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(layoutOrg, `"goal": "ship <fast> & safe"`, `"goal": "edited again"`, 1)
	if got := readFile(t, path); got != want {
		t.Fatalf("second save changed more than the edit:\n%s", got)
	}
}

// TestAppRejectedSaveRestoresTheExactBytes: when monomind rejects an edit
// the file is put back byte for byte.
func TestAppRejectedSaveRestoresTheExactBytes(t *testing.T) {
	echoOrgCLI(t, true)
	a, root := newOrgDesignApp(t)
	path := seedLayoutOrg(t, root)
	d, err := orgdesign.Load(root, "sec-org")
	if err != nil {
		t.Fatal(err)
	}
	title := "X"
	if _, err := d.UpdateRole("coder", orgdesign.RolePatch{Title: &title}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.saveOrgDoc(root, d, false); err == nil {
		t.Fatal("a rejected edit reported success")
	}
	if got := readFile(t, path); got != layoutOrg {
		t.Fatalf("rollback changed the file:\n%s", got)
	}
}
