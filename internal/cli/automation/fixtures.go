package automation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/spf13/cobra"
)

// `automation test`: validate the package, then run each fixture
// (tests/fixtures/<name>.html, <name> = <action>[.<variant>]) in a headless
// browser: the action runs through action.ExecuteDef in safe mode on a page
// that serves the fixture for every document URL, and its records must equal
// tests/<name>.expect.json. Inputs come from tests/<name>.inputs.json, else
// from same-named fields of the first expected record. Anything that stops
// the comparison (no browser, no expect file, missing inputs, a side-effect
// stop) is reported "skipped", never "pass".

// fixtureResult is one row of `automation test --json` "results".
// Status is pass | fail | skipped; OK is false only for fail, so a missing
// browser never fails CI.
type fixtureResult struct {
	Action  string `json:"action"`
	Fixture string `json:"fixture"`
	OK      bool   `json:"ok"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

func (d Deps) newAutomationTestCmd() *cobra.Command {
	var live, full bool
	cmd := &cobra.Command{
		Use:   "test <id|dir> [action]",
		Short: "Validate an automation and check its fixture snapshots",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if live {
				return errors.New("automation test --live is not supported yet; use 'record verify' to replay against the browser")
			}
			pkg, err := d.openTestTarget(args[0])
			if err != nil {
				return err
			}
			only := ""
			if len(args) == 2 {
				only = args[1]
			}
			runner, closeRunner := newBrowserFixtureRunner(full)
			defer closeRunner()
			results := runAutomationTests(pkg, only, runner)
			out := cmd.OutOrStdout()
			failed := 0
			for _, r := range results {
				if !r.OK {
					failed++
				}
			}
			if d.JSONOutput() {
				if err := writeJSONTo(out, map[string]any{"results": results}); err != nil {
					return err
				}
			} else {
				table := d.NewTable(out, []string{"Action", "Fixture", "Result", "Message"}, nil)
				for _, r := range results {
					table.Append([]string{r.Action, orDash(r.Fixture), r.Status, truncateStr(r.Message, 80)})
				}
				table.Render()
			}
			if failed > 0 {
				// The results already say why; with --json no second
				// {"error"} object may follow them on stdout.
				return d.Reported(fmt.Errorf("%d test(s) failed", failed))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "Run past side-effect steps (safe: fixtures are served in-process, nothing leaves the machine)")
	cmd.Flags().BoolVar(&live, "live", false, "Run against the real site in the browser (not supported yet)")
	return cmd
}

// openTestTarget accepts an installed id or a package directory.
func (d Deps) openTestTarget(target string) (*automation.Package, error) {
	if st, err := os.Stat(target); err == nil && st.IsDir() {
		return automation.OpenDir(target)
	}
	reg, err := d.OpenRegistry()
	if err != nil {
		return nil, err
	}
	return reg.Get(target)
}

// runAutomationTests returns the validation row followed by one row per
// fixture of the selected action(s). Always non-nil.
func runAutomationTests(pkg *automation.Package, only string, run fixtureRunner) []fixtureResult {
	results := []fixtureResult{}
	issues := automation.Validate(pkg)
	errs, warns := 0, 0
	for _, is := range issues {
		switch is.Severity {
		case "error":
			errs++
		case "warning":
			warns++
		} // "info" notes are not counted
	}
	msg := fmt.Sprintf("validate: %d error(s), %d warning(s)", errs, warns)
	if errs > 0 {
		for _, is := range issues {
			if is.Severity == "error" {
				msg += "; " + is.File + " " + is.Code + ": " + is.Message
				break
			}
		}
	}
	valAction := only
	if valAction == "" {
		valAction = "*"
	}
	status := "pass"
	if errs > 0 {
		status = "fail"
	}
	results = append(results, fixtureResult{Action: valAction, OK: errs == 0, Status: status, Message: msg})

	for _, fx := range listFixtures(pkg.FS, pkg.Manifest.Actions) {
		if only != "" && fx.action != only {
			continue
		}
		results = append(results, runFixture(pkg, fx, run))
	}
	return results
}

type fixtureFile struct {
	action, name, htmlPath, expectPath string
}

// listFixtures finds the tests of a package. A test <name> (= <action> or
// <action>.<variant>) exists when tests/<name>.expect.json exists, or when
// tests/fixtures/<name>.html exists and <action> is one of actions. Other
// fixture files are pages that tests/<name>.routes.json serves.
func listFixtures(fsys fs.FS, actions []string) []fixtureFile {
	var out []fixtureFile
	if fsys == nil {
		return out
	}
	isAction := map[string]bool{}
	for _, a := range actions {
		isAction[a] = true
	}
	names := map[string]bool{}
	entries, _ := fs.ReadDir(fsys, "tests")
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".expect.json"); ok && !e.IsDir() {
			names[n] = true
		}
	}
	entries, _ = fs.ReadDir(fsys, "tests/fixtures")
	for _, e := range entries {
		n, ok := strings.CutSuffix(e.Name(), ".html")
		act, _, _ := strings.Cut(n, ".")
		if ok && !e.IsDir() && isAction[act] {
			names[n] = true
		}
	}
	for name := range names {
		act, _, _ := strings.Cut(name, ".")
		fx := fixtureFile{action: act, name: name}
		if _, err := fs.Stat(fsys, "tests/fixtures/"+name+".html"); err == nil {
			fx.htmlPath = "tests/fixtures/" + name + ".html"
		}
		if _, err := fs.Stat(fsys, "tests/"+name+".expect.json"); err == nil {
			fx.expectPath = "tests/" + name + ".expect.json"
		}
		out = append(out, fx)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// fixtureInputs builds the run's params: tests/<name>.inputs.json when
// present, then same-named fields of the first expected record for any
// required input still missing. Returns the required inputs left unset.
func fixtureInputs(fsys fs.FS, name string, def *action.ActionDef, want interface{}) (map[string]interface{}, []string) {
	inputs := map[string]interface{}{}
	if b, err := fs.ReadFile(fsys, "tests/"+name+".inputs.json"); err == nil {
		_ = json.Unmarshal(b, &inputs)
	}
	var first map[string]interface{}
	switch w := want.(type) {
	case []interface{}:
		if len(w) > 0 {
			first, _ = w[0].(map[string]interface{})
		}
	case map[string]interface{}:
		first = w
	}
	var missing []string
	if def.Inputs == nil {
		return inputs, nil
	}
	for _, in := range parseInputs(def.Inputs.Required, true) {
		if _, ok := inputs[in.Name]; ok {
			continue
		}
		if v, ok := first[in.Name]; ok {
			inputs[in.Name] = v
			continue
		}
		missing = append(missing, in.Name)
	}
	return inputs, missing
}

// outputMatches compares through JSON (numbers and nulls as a node's output
// serialises them). The expectation is either the list of records, or one
// record (a single-record run, or the records under their output name, e.g.
// {"items":[…]}).
func outputMatches(items []map[string]interface{}, want interface{}) bool {
	norm := func(v interface{}) interface{} {
		b, _ := json.Marshal(v)
		var out interface{}
		_ = json.Unmarshal(b, &out)
		return out
	}
	w := norm(want)
	if items == nil {
		items = []map[string]interface{}{}
	}
	if reflect.DeepEqual(norm(items), w) {
		return true
	}
	if len(items) == 1 && reflect.DeepEqual(norm(items[0]), w) {
		return true
	}
	if m, ok := w.(map[string]interface{}); ok && len(m) == 1 {
		for _, v := range m {
			return reflect.DeepEqual(norm(items), v)
		}
	}
	return false
}
