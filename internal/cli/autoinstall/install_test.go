package autoinstall

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/automation"
)

// resultErr and reportedErr stand in for the CLI's installResultError and reportedError.
type resultErr struct {
	err error
	res *automation.InstallResult
}

func (e *resultErr) Error() string { return e.err.Error() }
func (e *resultErr) Unwrap() error { return e.err }

type reportedErr struct{ error }

type notFoundErr struct{ msg string }

func (e notFoundErr) Error() string { return e.msg }

// testDeps is the CLI as the pipeline sees it, on a registry of its own.
func testDeps(t *testing.T, jsonOut bool) Deps {
	t.Helper()
	reg, err := automation.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return Deps{
		JSONOutput:      func() bool { return jsonOut },
		OpenRegistry:    func() (*automation.Registry, error) { return reg, nil },
		StdinIsTerminal: func() bool { return false },
		WithInstallResult: func(err error, res *automation.InstallResult) error {
			if err == nil || res == nil {
				return err
			}
			return &resultErr{err, res}
		},
		InstallResultOf: func(err error) *automation.InstallResult {
			var re *resultErr
			if errors.As(err, &re) {
				return re.res
			}
			return nil
		},
		Reported: func(err error) error { return reportedErr{err} },
		NotFound: func(format string, a ...any) error { return notFoundErr{fmt.Sprintf(format, a...)} },
	}
}

// writePackage writes a small valid package directory and returns it.
func writePackage(t *testing.T, id string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(filepath.Join(dir, "actions"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema":"monoagent.automation/v1","id":"` + id + `","name":"Plain","version":"1.0.0",
		"site":{"startUrl":"https://example.com/","domains":["example.com"]},"permissions":{"steps":["log"],"scripts":[]},
		"actions":["a"],"policy":{"tier":"standard"}}`
	action := `{"actionType":"a","automation":"` + id + `","sideEffects":"none","steps":[{"id":"s","type":"log"}]}`
	if err := os.WriteFile(filepath.Join(dir, "automation.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "actions", "a.json"), []byte(action), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func run(t *testing.T, cmd *cobra.Command, stdin string, args ...string) (string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	cmd.SetArgs(args)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	return out.String(), errb.String(), err
}

func TestReviewPrintsScriptSources(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	printScriptSources(&b, map[string]string{"grab.js": "return document.title"})
	s := b.String()
	if !strings.Contains(s, "WARNING") || !strings.Contains(s, "return document.title") || !strings.Contains(s, "scripts/grab.js") {
		t.Fatalf("output:\n%s", s)
	}
}

func TestRunInstallConfirmation(t *testing.T) {
	t.Parallel()
	d := testDeps(t, false)
	review := &automation.InstallResult{ID: "p", Version: "1.0.0", DryRun: true, SHA256: "abc",
		Review: automation.Review{ReplaceRequired: true}}
	for _, tc := range []struct {
		name   string
		opts   automation.InstallOptions
		c      Confirmer
		want   error
		wantN  int
		expect string
	}{
		{name: "dry run reviews only", opts: automation.InstallOptions{DryRun: true}, wantN: 1},
		{name: "nobody to ask", want: ErrConfirmationRequired, wantN: 0},
		{name: "answered no", c: Confirmer{Interactive: true, In: strings.NewReader("n\n"), Out: io.Discard}, wantN: 1},
		{name: "answered yes", c: Confirmer{Interactive: true, In: strings.NewReader("yes\n"), Out: io.Discard}, wantN: 2, expect: "abc"},
		{name: "--yes", c: Confirmer{Yes: true}, wantN: 1},
	} {
		var calls []automation.InstallOptions
		res, err := d.RunInstall(tc.opts, tc.c, func(o automation.InstallOptions) (*automation.InstallResult, error) {
			calls = append(calls, o)
			if o.DryRun {
				return review, nil
			}
			return &automation.InstallResult{ID: "p", Installed: true}, nil
		})
		if len(calls) != tc.wantN {
			t.Fatalf("%s: %d calls, want %d", tc.name, len(calls), tc.wantN)
		}
		for _, o := range calls {
			if o.Source != automation.SourceImported {
				t.Fatalf("%s: source %q, want imported by default", tc.name, o.Source)
			}
		}
		switch {
		case tc.want != nil:
			if !errors.Is(err, tc.want) {
				t.Fatalf("%s: err %v", tc.name, err)
			}
		case tc.name == "answered no":
			if err == nil || err.Error() != "install cancelled" {
				t.Fatalf("%s: err %v", tc.name, err)
			}
		case err != nil || res == nil:
			t.Fatalf("%s: err %v res %v", tc.name, err, res)
		}
		if tc.expect != "" {
			last := calls[len(calls)-1]
			if last.ExpectSHA256 != tc.expect || !last.Replace || last.DryRun {
				t.Fatalf("%s: install options %+v", tc.name, last)
			}
		}
	}
}

func TestRunInstallKeepsFailedResult(t *testing.T) {
	t.Parallel()
	d := testDeps(t, false)
	failed := &automation.InstallResult{ID: "p", Issues: []automation.IssueJSON{{Severity: "error", Code: "x"}}}
	_, err := d.RunInstall(automation.InstallOptions{}, Confirmer{Yes: true},
		func(automation.InstallOptions) (*automation.InstallResult, error) { return failed, errors.New("boom") })
	if d.InstallResultOf(err) != failed {
		t.Fatalf("err %v lost the result", err)
	}
	cmd := &cobra.Command{}
	var errb bytes.Buffer
	cmd.SetErr(&errb)
	d.PrintFailedIssues(cmd, err)
	if !strings.Contains(errb.String(), "1 issue(s):") || !strings.Contains(errb.String(), "x: ") {
		t.Fatalf("stderr: %q", errb.String())
	}
}

func TestWriteHashedRemovesPartialFile(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "out.mpkg")
	if _, err := WriteHashed(file, func(w io.Writer) error {
		io.WriteString(w, "half")
		return errors.New("disk full")
	}); err == nil {
		t.Fatal("want the write error")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("partial file left: %v", err)
	}
	sum, err := WriteHashed(file, func(w io.Writer) error { _, err := io.WriteString(w, "abc"); return err })
	if err != nil || sum != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("sum %q err %v", sum, err)
	}
}

func TestValidatePackInstallExportLifecycle(t *testing.T) {
	t.Parallel()
	d := testDeps(t, true)
	dir := writePackage(t, "plain")
	out, _, err := run(t, d.NewValidateCmd(), "", dir)
	if err != nil || !strings.Contains(out, `"ok": true`) {
		t.Fatalf("validate: %v %s", err, out)
	}

	file := filepath.Join(t.TempDir(), "plain.mpkg")
	out, _, err = run(t, d.NewPackCmd(), "", dir, "-o", file)
	var packed map[string]string
	if err != nil || json.Unmarshal([]byte(out), &packed) != nil || packed["file"] != file || len(packed["sha256"]) != 64 {
		t.Fatalf("pack: %v %s", err, out)
	}

	// --json cannot prompt: an install needs --yes.
	if _, _, err = run(t, d.NewInstallCmd(), "", file); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("unconfirmed install: %v", err)
	}
	out, _, err = run(t, d.NewInstallCmd(), "", file, "--yes")
	var res automation.InstallResult
	if err != nil || json.Unmarshal([]byte(out), &res) != nil || !res.Installed || res.ID != "plain" {
		t.Fatalf("install: %v %s", err, out)
	}

	exported := filepath.Join(t.TempDir(), "out.mpkg")
	out, _, err = run(t, d.NewExportCmd(), "", "plain", "-o", exported)
	if err != nil || !strings.Contains(out, `"sha256"`) {
		t.Fatalf("export: %v %s", err, out)
	}

	cmds := map[string]*cobra.Command{}
	for _, c := range d.NewLifecycleCmds() {
		cmds[c.Name()] = c
	}
	out, _, err = run(t, cmds["disable"], "", "plain")
	if err != nil || !strings.Contains(out, `"ok": true`) || !strings.Contains(out, `"enabled": false`) {
		t.Fatalf("disable: %v %s", err, out)
	}
	var nf notFoundErr
	if _, _, err = run(t, cmds["restore"], "", "plain"); !errors.As(err, &nf) || !strings.Contains(err.Error(), "library install automation plain") {
		t.Fatalf("restore: %v", err)
	}
	out, _, err = run(t, cmds["uninstall"], "", "plain")
	if err != nil || !strings.Contains(out, `"info": null`) {
		t.Fatalf("uninstall: %v %s", err, out)
	}
}

func TestValidateJSONFailureIsReported(t *testing.T) {
	t.Parallel()
	d := testDeps(t, true)
	file := filepath.Join(t.TempDir(), "a.json")
	if err := os.WriteFile(file, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := run(t, d.NewValidateCmd(), "", file)
	var rep reportedErr
	if !errors.As(err, &rep) || !strings.Contains(out, `"invalid_json"`) || !strings.Contains(out, `"ok": false`) {
		t.Fatalf("err %v out %s", err, out)
	}
}

func TestExportDomainFlags(t *testing.T) {
	t.Parallel()
	var f DomainFlags
	cmd := &cobra.Command{}
	f.Register(cmd)
	if err := cmd.ParseFlags([]string{"--domains", "a.com, ,b.com", "--use-suggested-domains"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Resolve(nil, "x"); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err %v", err)
	}
	f.useSuggested = false
	if got, err := f.Resolve(nil, "x"); err != nil || strings.Join(got, "|") != "a.com|b.com" {
		t.Fatalf("got %v err %v", got, err)
	}
}
