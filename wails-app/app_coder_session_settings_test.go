//go:build !windows

package main

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/coderhost"
)

func TestSettingsPatchAcceptsOnlyWhatThePaneSendsAndSendsTheCheckedText(t *testing.T) {
	a, log, _ := settingsFixture(t, true, storedDefault)
	a.SetCoderSessionSettings("c1", `{"checkin_minutes":30,"skip_if_unchanged":true,"autonomy_classes":{"ambiguity":{"who":"user","fallback":"park"}}}`, "true")
	got := ran(t, log)
	if len(got) != 1 || !strings.Contains(got[0], `--patch={"autonomy_classes":{"ambiguity":{"who":"user","fallback":"park"}},"checkin_minutes":30,"skip_if_unchanged":true}`) || !strings.Contains(got[0], "--unattended=true") {
		t.Fatalf("calls %q", got)
	}
}

// The widening judgement and the write are one compare-and-set: the digest of the settings the dialog judged goes to the CLI, which
// refuses the write when the stored settings are not those any more (a change a patch makes to no autonomy key is not judged and
// carries none).
func TestAutonomyPatchesCarryTheDigestOfTheSettingsTheyWereJudgedAgainst(t *testing.T) {
	a, log, _ := settingsFixture(t, true, storedDefault)
	a.SetCoderSessionSettings("c1", `{"autonomy_preset":"ask-always"}`, "")
	a.SetCoderSessionSettings("c1", `{"checkin_minutes":30}`, "")
	a.SetCoderSessionSettings("c1", `{"autonomy_preset":"decide-and-log"}`, "") // widening: confirmed (yes), and still compare-and-set
	got := ran(t, log)
	if len(got) != 3 || !strings.Contains(got[0], "--expect=d-default") || strings.Contains(got[1], "--expect") || !strings.Contains(got[2], "--expect=d-default") {
		t.Fatalf("calls %q", got)
	}
	// stored settings with no digest cannot be compared: the person is asked (widening) and the write is not compare-and-set
	a, log, said := settingsFixture(t, true, `{"settings":{"autonomy_preset":"ask-on-risk","autonomy_classes":{}}}`)
	a.SetCoderSessionSettings("c1", `{"autonomy_preset":"decide-and-log"}`, "")
	if got := ran(t, log); len(got) != 1 || strings.Contains(got[0], "--expect") || len(*said) != 1 {
		t.Fatalf("calls %q dialogs %q", got, *said)
	}
}

func TestSettingsPatchRefusesEverySettingTheConfirmedSwitchesOwn(t *testing.T) {
	a, log, said := authorityFixture(t, true, nil)
	bad := []string{`{"agent_approval":"lead","approval_ceiling_usd":1000000}`, `{"approval_ceiling_usd":5}`, `{"mission":true}`, `{"chat_mode":"mission"}`,
		`{"max_depth":9}`, `{"mission_directions":6}`, `{"mission_worker_reservations":true}`, `{"owed_stop_notice":{}}`, `{"checkin_minutes":5,"agent_approval":"lead"}`,
		`[]`, `null`, `"x"`, `5`, `{`, `{"agent_approval":"off","agent_approval":"lead"}`, `{"x":1}`, `{"checkin_minutes":5} {"agent_approval":"lead"}`}
	// every operator-only setting that the pane does not send is refused, by name (a new one is refused until it is decided)
	for _, k := range coderhost.OperatorOnly {
		if !paneSettingKeys[k] {
			bad = append(bad, `{"`+k+`":1}`)
		}
	}
	for _, p := range bad {
		if out := a.SetCoderSessionSettings("c1", p, ""); !strings.Contains(out, "error") {
			t.Errorf("patch %s was accepted: %s", p, out)
		}
	}
	if out := a.SetCoderSessionSettings("c1", `{"checkin_minutes":5}`, "maybe"); !strings.Contains(out, "error") {
		t.Errorf("unattended=maybe accepted: %s", out)
	}
	if got := ran(t, log); len(got) != 0 || len(*said) != 0 {
		t.Fatalf("the CLI ran for a refused patch: %q", got)
	}
}

// The operator-only settings the pane sends are listed here: a new one is a decision, and goes with the review.
func TestThePaneSendsOnlyTheseOperatorOnlySettings(t *testing.T) {
	var got []string
	for k := range paneSettingKeys {
		for _, o := range coderhost.OperatorOnly {
			if o == k {
				got = append(got, k)
			}
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "autonomy_classes,autonomy_preset,checkin_limit,max_children,max_concurrent,mesh_accept" {
		t.Fatalf("operator-only settings the app lets through: %v", got)
	}
	// ...and which of them also need the native dialog (wideningsOf): a new operator-only key is decided here, in the review
	a, _, _ := settingsFixture(t, true, `{"settings":{"autonomy_preset":"ask-always","autonomy_classes":{}}}`)
	sample := map[string]string{"autonomy_preset": `"decide-and-log"`, "autonomy_classes": `{"ambiguity":{"who":"model","fallback":"take_default"}}`, "mesh_accept": `"project"`}
	var dialog []string
	for _, k := range got {
		v := sample[k]
		if v == "" {
			v = `"x"`
		}
		if wide, _ := a.wideningsOf("c1", `{"`+k+`":`+v+`}`); len(wide) > 0 {
			dialog = append(dialog, k)
		}
	}
	if strings.Join(dialog, ",") != "autonomy_classes,autonomy_preset,mesh_accept" {
		t.Fatalf("operator-only pane settings that ask the person: %v", dialog)
	}
}

// settingsFixture is authorityFixture whose `settings get` answers stored (the CLI's view), so the app can judge a patch by its result.
func settingsFixture(t *testing.T, yes bool, stored string) (a *App, log string, dialogs *[]string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "args.log")
	got := filepath.Join(dir, "stored.json")
	if err := os.WriteFile(got, []byte(stored), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := fakeCLI(t, `case "$*" in *"settings get"*) cat '`+got+`'; exit 0;; esac`+"\n"+`echo "$*" >> '`+log+"'\n"+`echo '{}'`+"\n")
	a, _ = newCLIChatApp(t, bin)
	if err := os.WriteFile(log, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var said []string
	old := confirmNative
	confirmNative = func(_ *App, title, body string) (bool, error) {
		said = append(said, title+": "+body)
		return yes, nil
	}
	t.Cleanup(func() { confirmNative = old })
	return a, log, &said
}

const (
	storedDefault   = `{"digest":"d-default","settings":{"autonomy_preset":"ask-on-risk","autonomy_classes":{},"mesh_accept":"subtree"}}`
	storedAskAlways = `{"digest":"d-ask-always","settings":{"autonomy_preset":"ask-always","autonomy_classes":{},"mesh_accept":"subtree"}}`
	// the person asked for a user/park answer on two kinds of question while the preset decides the rest by itself
	storedOverrides = `{"digest":"d-overrides","settings":{"autonomy_preset":"decide-and-log","autonomy_classes":{"ambiguity":{"who":"user","fallback":"park"},"scope_growth":{"who":"user","fallback":"park"}},"mesh_accept":"none"}}`
)

func TestWideningSettingsAskTheNativeDialogAndSaferValuesDoNot(t *testing.T) {
	type tc struct{ stored, patch string }
	widen := []tc{
		{storedDefault, `{"autonomy_preset":"decide-and-log"}`}, {storedDefault, `{"autonomy_preset":5}`},
		{storedDefault, `{"autonomy_classes":{"ambiguity":{"who":"model","fallback":"take_default"}}}`},
		{storedAskAlways, `{"autonomy_classes":{"ambiguity":{"who":"user","fallback":"take_default"}}}`},
		{storedAskAlways, `{"autonomy_preset":"ask-on-risk"}`},
		// a patched map REPLACES the stored one: this keeps ambiguity at user/park and drops scope_growth's override, which the
		// preset (decide-and-log) then gives to the model with a default
		{storedOverrides, `{"autonomy_classes":{"ambiguity":{"who":"user","fallback":"park"}}}`},
		{storedOverrides, `{"autonomy_classes":{}}`}, {storedOverrides, `{"autonomy_classes":null}`},
		{storedDefault, `{"mesh_accept":"project"}`}, {storedDefault, `{"mesh_accept":"subtree"}`}, {storedDefault, `{"checkin_minutes":5,"mesh_accept":"project"}`},
		{`{}`, `{"autonomy_preset":"ask-always"}`}, // the stored settings cannot be read: widening
	}
	for _, c := range widen {
		a, log, said := settingsFixture(t, true, c.stored)
		if out := a.SetCoderSessionSettings("c1", c.patch, ""); strings.Contains(out, "error") {
			t.Errorf("%s: %s", c.patch, out)
		}
		if len(*said) != 1 || len(ran(t, log)) != 1 {
			t.Errorf("%s over %s: dialogs %q calls %q", c.patch, c.stored, *said, ran(t, log))
		}
	}
	safe := []tc{
		{storedDefault, `{"autonomy_preset":"ask-always"}`}, {storedDefault, `{"autonomy_preset":"ask-on-risk"}`}, {storedDefault, `{"mesh_accept":"none"}`},
		{storedDefault, `{"autonomy_classes":{"ambiguity":{"who":"user","fallback":"park"}}}`},
		{storedOverrides, `{"autonomy_preset":"ask-always","autonomy_classes":{"ambiguity":{"who":"user","fallback":"park"}}}`},
		{storedOverrides, `{"autonomy_classes":{"ambiguity":{"who":"user","fallback":"park"},"scope_growth":{"who":"user","fallback":"park"}}}`},
		{storedDefault, `{"checkin_limit":3,"max_children":2,"max_concurrent":1,"checkin_minutes":30,"mesh_visible":true,"mesh_alias":"x"}`},
	}
	for _, c := range safe {
		a, log, said := settingsFixture(t, false, c.stored)
		if out := a.SetCoderSessionSettings("c1", c.patch, ""); strings.Contains(out, "error") {
			t.Errorf("%s: %s", c.patch, out)
		}
		if len(*said) != 0 || len(ran(t, log)) != 1 {
			t.Errorf("%s over %s: dialogs %q calls %q", c.patch, c.stored, *said, ran(t, log))
		}
	}
}

func TestWideningSettingsAreRefusedWithoutAYesWhereNoDialogCanBeShownAndForANonOperator(t *testing.T) {
	p := `{"autonomy_preset":"decide-and-log"}`
	a, _, _ := authorityFixture(t, false, nil)
	if out := a.SetCoderSessionSettings("c1", p, ""); !strings.Contains(out, "declined") {
		t.Fatalf("%s", out)
	}
	a, _, _ = authorityFixture(t, true, errNoNativeDialog)
	if out := a.SetCoderSessionSettings("c1", p, ""); !strings.Contains(out, "no_native_dialog") {
		t.Fatalf("%s", out)
	}
	// (the browser end-to-end host refuses this dialog by its title: TestTheE2EHostNeverConfirmsAnAuthorityTurnOn)
	a, log, said := authorityFixture(t, true, nil)
	opgate.SetForTest(t, func(bool) string { return "this process runs inside a coder session" })
	if out := a.SetCoderSessionSettings("c1", p, ""); !strings.Contains(out, "operator_only") {
		t.Fatalf("%s", out)
	}
	if len(*said) != 0 || len(ran(t, log)) != 0 {
		t.Fatalf("a dialog or the CLI ran: %q %q", *said, ran(t, log))
	}
}

func TestOperatorOnlyPaneSettingsNeedTheOperatorGate(t *testing.T) {
	a, log, _ := authorityFixture(t, true, nil)
	opgate.SetForTest(t, func(bool) string { return "this process runs inside a coder session" })
	if out := a.SetCoderSessionSettings("c1", `{"max_children":3}`, ""); !strings.Contains(out, "operator_only") {
		t.Fatalf("%s", out)
	}
	if out := a.SetCoderSessionSettings("c1", `{"checkin_minutes":15}`, ""); strings.Contains(out, "error") {
		t.Fatalf("a non-operator setting was refused: %s", out)
	}
	if got := ran(t, log); len(got) != 1 {
		t.Fatalf("calls %q", got)
	}
}

// Structural: every App function that writes settings through the CLI (the group "settings","set", or a --patch flag) is in the
// inventory below with the shape it must keep; a new writer, whatever it builds its patch from, fails here until it is decided.
func TestEverySettingsWriterIsInTheInventoryWithItsShape(t *testing.T) {
	inventory := map[string]string{
		"SetChatMissionToggle":    "fixed",    // --mission=on|off from a typed bool; the person's own header toggle (CLI-validated)
		"SetChatMode":             "fixed",    // --chat-mode=MODE; chosen before the first message, locked after (CLI-validated)
		"SetCoderSessionSettings": "filtered", // checkSettingsPatch: the pane's keys only; widening ones also need the native dialog
		"SetAgentApproval":        "gated",    // operator gate + native dialog
	}
	scan := func(f srcFile) (writers []string, bad []string) {
		for _, d := range f.file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			isWriter := false
			literalGroups(srcFile{path: f.path, file: &ast.File{Name: f.file.Name, Decls: []ast.Decl{fd}}, fset: f.fset}, func(g []string, _ token.Pos, _ string) {
				isWriter = isWriter || (has(g, "settings") && has(g, "set"))
			})
			// whatever way the argv is put together (concatenated pieces, a Fields/Split of one string), the words of every literal
			// in the function are read: a settings verb or a --patch flag anywhere in it makes it a writer
			words := literalWords(fd)
			isWriter = isWriter || (words["settings"] && words["set"]) || words["--patch"]
			if splitsLiteralIntoArgv(fd) {
				bad = append(bad, fd.Name.Name+" builds an argv by splitting a string at run time")
			}
			if !isWriter {
				continue
			}
			writers = append(writers, fd.Name.Name)
			if _, ok := inventory[fd.Name.Name]; !ok {
				bad = append(bad, fd.Name.Name+" writes settings and is not in the inventory")
			}
		}
		return
	}
	var all []string
	for _, f := range nonTestSources(t) {
		w, bad := scan(f)
		all = append(all, w...)
		for _, b := range bad {
			t.Errorf("%s: %s", f.path, b)
		}
	}
	sort.Strings(all)
	if strings.Join(all, ",") != "SetAgentApproval,SetChatMissionToggle,SetChatMode,SetCoderSessionSettings" {
		t.Fatalf("settings writers: %v", all)
	}
	for _, f := range nonTestSources(t) {
		for _, d := range f.file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || inventory[fd.Name.Name] == "" {
				continue
			}
			first := func(name string) token.Pos {
				cs, _ := callsOf(fd, name)
				if len(cs) == 0 {
					return token.NoPos
				}
				return cs[0]
			}
			run := first("jsonResult")
			switch inventory[fd.Name.Name] {
			case "filtered":
				if c := first("checkSettingsPatch"); c == token.NoPos || run == token.NoPos || c > run {
					t.Errorf("%s must check the patch before it runs the CLI", fd.Name.Name)
				}
				if g, c := first("Refusal"), first("authorityConfirm"); g == token.NoPos || c == token.NoPos || !(g < c && c < run) {
					t.Errorf("%s must pass the operator gate and the native dialog (for the widening keys) before the CLI", fd.Name.Name)
				}
			case "gated":
				if g, c := first("Refusal"), first("authorityConfirm"); g == token.NoPos || c == token.NoPos || !(g < c && c < run) {
					t.Errorf("%s must pass the operator gate and the native dialog before the CLI", fd.Name.Name)
				}
			case "fixed":
				ast.Inspect(fd, func(n ast.Node) bool {
					if s, ok := strLit(n); ok && strings.HasPrefix(s, "--patch") {
						t.Errorf("%s builds a patch", fd.Name.Name)
					}
					return true
				})
			}
		}
	}
	// the scan sees a writer whose patch comes from a variable
	_, bad := scan(parseSource(t, "mutant.go", []byte(`package main
func (a *App) SetSneaky(p string) string { args := []string{"coder", "sessions", "settings", "set"}; return a.jsonResult(append(args, p)...) }`)))
	if len(bad) != 1 {
		t.Error("the scan does not see a settings writer that is not in the inventory")
	}
	for name, src := range map[string]string{
		"Fields": `package main
func (a *App) SetSneaky3(p string) string { return a.jsonResult(strings.Fields("coder sessions settings set --patch="+p)...) }`,
		"folded": `package main
func (a *App) SetSneaky4(p string) string { args := []string{"coder", "sessions " + "settings"}; return a.jsonResult(append(args, "se"+"t", p)...) }`,
		"splitOnly": `package main
func (a *App) SetSneaky5(p string) string { return a.jsonResult(strings.Split("coder x "+p, " ")...) }`,
	} {
		if _, bad := scan(parseSource(t, "mutant.go", []byte(src))); len(bad) == 0 {
			t.Errorf("the scan does not see the %s mutant", name)
		}
	}
	_, bad = scan(parseSource(t, "mutant.go", []byte(`package main
func (a *App) SetSneaky2(p string) string { return a.jsonResult("coder", "x", "--patch="+p) }`)))
	if len(bad) != 1 {
		t.Error("the scan does not see a --patch writer")
	}
}

var wordRe = regexp.MustCompile(`--?[A-Za-z0-9_]+|[A-Za-z0-9_]+`)

// literalWords are the words of every string literal in fd (so "coder sessions settings set --patch=" yields coder, sessions,
// settings, set and --patch), whichever expression the literal is in.
func literalWords(fd *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	add := func(s string) {
		for _, w := range wordRe.FindAllString(s, -1) {
			out[w] = true
		}
	}
	ast.Inspect(fd, func(n ast.Node) bool {
		if s, ok := strLit(n); ok {
			add(s)
		}
		if s, ok := foldedLiterals(n); ok { // "se" + "t" is the word set
			add(s)
		}
		return true
	})
	return out
}

// foldedLiterals is the text of a "+" expression made only of string literals.
func foldedLiterals(n ast.Node) (string, bool) {
	if s, ok := strLit(n); ok {
		return s, true
	}
	b, ok := n.(*ast.BinaryExpr)
	if !ok || b.Op != token.ADD {
		return "", false
	}
	l, lok := foldedLiterals(b.X)
	r, rok := foldedLiterals(b.Y)
	return l + r, lok && rok
}

// splitsLiteralIntoArgv says whether fd cuts a string that has a literal in it into words (strings.Fields, Split, SplitN, ...) and
// also runs the CLI: that is how an argv can be built at run time with no literal of its own.
func splitsLiteralIntoArgv(fd *ast.FuncDecl) bool {
	splits, runs := false, false
	ast.Inspect(fd, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := c.Fun.(type) {
		case *ast.SelectorExpr:
			if id, ok := fun.X.(*ast.Ident); ok && id.Name == "strings" && (strings.HasPrefix(fun.Sel.Name, "Fields") || strings.HasPrefix(fun.Sel.Name, "Split")) {
				if len(c.Args) > 0 { // the string that is cut (the separator of Split is not it)
					ast.Inspect(c.Args[0], func(m ast.Node) bool {
						if _, ok := strLit(m); ok {
							splits = true
						}
						return true
					})
				}
			}
			runs = runs || fun.Sel.Name == "jsonResult" || fun.Sel.Name == "jsonCLI" || fun.Sel.Name == "Command" || fun.Sel.Name == "CommandContext" || strings.HasPrefix(fun.Sel.Name, "runCLI")
		case *ast.Ident:
			runs = runs || strings.HasPrefix(fun.Name, "runCLI")
		}
		return true
	})
	return splits && runs
}
