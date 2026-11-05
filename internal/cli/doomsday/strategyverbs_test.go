package doomsday

import (
	"bytes"
	"context"
	"encoding/json"
	"go/build"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

type fakeStrategyReader struct {
	status   StrategyStatusView
	epochs   StrategyEpochsView
	dirs     StrategyDirectionsView
	calls    []string
	lastGen  int64
	lastLim  int
	failWith error
}

func (f *fakeStrategyReader) ReadStatus(_ context.Context, gen int64) (StrategyStatusView, error) {
	f.calls, f.lastGen = append(f.calls, "status"), gen
	return f.status, f.failWith
}
func (f *fakeStrategyReader) ReadEpochs(_ context.Context, gen int64, limit int) (StrategyEpochsView, error) {
	f.calls, f.lastGen, f.lastLim = append(f.calls, "epochs"), gen, limit
	return f.epochs, f.failWith
}
func (f *fakeStrategyReader) ReadDirections(_ context.Context, gen int64, limit int) (StrategyDirectionsView, error) {
	f.calls, f.lastGen, f.lastLim = append(f.calls, "directions"), gen, limit
	return f.dirs, f.failWith
}

func runStrategy(t *testing.T, d StrategyDeps, args ...string) (string, error) {
	t.Helper()
	cmd := StrategyCommands(d)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

// A default install has no reader: every verb says "off" and reads nothing.
func TestStrategyVerbsPrintOffWithoutAReader(t *testing.T) {
	for _, v := range StrategyVerbs {
		out, err := runStrategy(t, StrategyDeps{}, v)
		if err != nil || strings.TrimSpace(out) != "off" {
			t.Fatalf("%s: out=%q err=%v", v, out, err)
		}
		out, err = runStrategy(t, StrategyDeps{JSONOutput: func() bool { return true }}, v)
		if err != nil || strings.TrimSpace(out) != `{"off":true}` {
			t.Fatalf("%s json: out=%q err=%v", v, out, err)
		}
	}
}

// The group has exactly the three verbs, each marked read-only, none of them an acting verb.
func TestStrategyVerbSetIsReadOnly(t *testing.T) {
	root := StrategyCommands(StrategyDeps{})
	var names []string
	for _, c := range root.Commands() {
		names = append(names, c.Name())
		if c.Annotations[AnnotationReadOnly] != "true" {
			t.Errorf("%s is not marked read-only", c.Name())
		}
		for _, bad := range []string{"start", "stop", "approve", "finish", "begin", "reverify", "rubric", "propose", "prune", "set", "write"} {
			if strings.Contains(c.Name(), bad) {
				t.Errorf("verb %q looks like an acting verb", c.Name())
			}
		}
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if strings.Contains(f.Name, "yes") || strings.Contains(f.Name, "force") || strings.Contains(f.Name, "lift") {
				t.Errorf("%s has an acting flag --%s", c.Name(), f.Name)
			}
		})
	}
	if len(names) != len(StrategyVerbs) {
		t.Fatalf("verbs %v, want %v", names, StrategyVerbs)
	}
	for _, want := range StrategyVerbs {
		if _, _, err := root.Find([]string{want}); err != nil {
			t.Errorf("missing verb %s", want)
		}
	}
}

// The reader interface has read methods only: a method that writes would have to be named like one of them.
func TestStrategyReaderInterfaceHasReadMethodsOnly(t *testing.T) {
	ty := reflect.TypeOf((*StrategyReader)(nil)).Elem()
	if ty.NumMethod() != 3 {
		t.Fatalf("reader has %d methods", ty.NumMethod())
	}
	for i := 0; i < ty.NumMethod(); i++ {
		if !strings.HasPrefix(ty.Method(i).Name, "Read") {
			t.Errorf("method %s does not read", ty.Method(i).Name)
		}
	}
}

func TestStrategyVerbsCallTheReaderAndBoundTheLimit(t *testing.T) {
	f := &fakeStrategyReader{}
	d := StrategyDeps{Reader: func() StrategyReader { return f }, JSONOutput: func() bool { return true }}
	if _, err := runStrategy(t, d, "epochs", "--limit", "100000", "--generation", "3"); err != nil {
		t.Fatal(err)
	}
	if f.lastLim != StrategyMaxLimit || f.lastGen != 3 {
		t.Fatalf("limit %d gen %d", f.lastLim, f.lastGen)
	}
	if _, err := runStrategy(t, d, "directions"); err != nil || f.lastLim != StrategyDefaultLimit {
		t.Fatalf("default limit %d err %v", f.lastLim, err)
	}
	if _, err := runStrategy(t, d, "directions", "--limit", "-5"); err != nil || f.lastLim != StrategyDefaultLimit {
		t.Fatalf("negative limit %d err %v", f.lastLim, err)
	}
	if _, err := runStrategy(t, d, "status", "--generation", "-1"); err == nil {
		t.Fatal("a negative generation must be refused")
	}
	for _, c := range f.calls {
		if c != "status" && c != "epochs" && c != "directions" {
			t.Fatalf("unexpected call %s", c)
		}
	}
}

// Hostile text in a title or a pin cannot reach the terminal as control sequences, and is capped.
func TestStrategyRenderRedactsAndCaps(t *testing.T) {
	evil := "\x1b[31mred\x1b]0;owned\x07 " + strings.Repeat("x", 500) + "\nINJECTED LINE"
	f := &fakeStrategyReader{
		dirs:   StrategyDirectionsView{Generation: 1, Total: 1, Directions: []DirectionView{{ID: "d-1", State: "active", Title: evil}}},
		epochs: StrategyEpochsView{Generation: 1, Total: 1, Epochs: []EpochView{{Seq: 1, Trigger: "timer", State: "applied", ModelPin: evil}}},
	}
	d := StrategyDeps{Reader: func() StrategyReader { return f }}
	for _, v := range []string{"directions", "epochs"} {
		out, err := runStrategy(t, d, v)
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(out, "\x1b\x07") || strings.Contains(out, "\nINJECTED") || strings.Contains(out, strings.Repeat("x", strategyTitleCap+10)) {
			t.Fatalf("%s: unredacted output %q", v, out)
		}
	}
}

// A tampered row is reported by every verb, never shown as an empty list.
func TestStrategyTamperedIsReportedNotHidden(t *testing.T) {
	f := &fakeStrategyReader{
		status: StrategyStatusView{Generation: 2, Tampered: true},
		epochs: StrategyEpochsView{Generation: 2, Tampered: true},
		dirs:   StrategyDirectionsView{Generation: 2, Tampered: true},
	}
	d := StrategyDeps{Reader: func() StrategyReader { return f }}
	for _, v := range StrategyVerbs {
		out, err := runStrategy(t, d, v)
		if err != nil || !strings.Contains(out, "TAMPERED") {
			t.Fatalf("%s: out=%q err=%v", v, out, err)
		}
	}
	dj := StrategyDeps{Reader: d.Reader, JSONOutput: func() bool { return true }}
	out, _ := runStrategy(t, dj, "epochs")
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil || got["tampered"] != true {
		t.Fatalf("json %q err %v", out, err)
	}
}

func TestStrategyReadErrorShowsNoStoreText(t *testing.T) {
	f := &fakeStrategyReader{failWith: context.DeadlineExceeded}
	d := StrategyDeps{Reader: func() StrategyReader { return f }}
	_, err := runStrategy(t, d, "status")
	if err == nil || strings.Contains(err.Error(), "deadline") {
		t.Fatalf("err=%v", err)
	}
}

func TestRedactTextBasics(t *testing.T) {
	if got := RedactText("  a\t\tb \x00 c  ", 10); got != "a b c" {
		t.Fatalf("%q", got)
	}
	if got := RedactText(strings.Repeat("é", 20), 5); got != "ééééé..." {
		t.Fatalf("%q", got)
	}
	if got := RedactText("‮abc", 10); got != "abc" {
		t.Fatalf("bidi override kept: %q", got)
	}
}

// The strategy layer's host and reader symbols and its library imports live in an exact set of the wiring's files. A new file that imports a strategy library or declares a host, reader or sink fails until it is decided here.
func TestStrategySymbolsLiveOnlyInTheWiringFiles(t *testing.T) {
	const strategyPrefix = "github.com/monoes/mono-agent/internal/doomsday/strategy"
	ctx := build.Default
	pkg, err := ctx.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	fset := token.NewFileSet()
	for _, f := range pkg.GoFiles {
		af, err := parser.ParseFile(fset, filepath.Join(pkg.Dir, f), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		hit := false
		for _, im := range af.Imports {
			if p := strings.Trim(im.Path.Value, `"`); strings.HasPrefix(p, strategyPrefix) && !strings.HasSuffix(p, "/types") {
				hit = true
			}
		}
		for name := range af.Scope.Objects {
			if strings.HasPrefix(name, "StrategyHost") || strings.HasPrefix(name, "NewStrategyHost") || strings.HasPrefix(name, "NewStrategyReader") ||
				strings.HasPrefix(name, "strategyReader") || strings.HasPrefix(name, "strategySink") {
				hit = true
			}
		}
		if hit {
			got = append(got, f)
		}
	}
	sort.Strings(got)
	want := "strategyengine.go,strategyreader.go,strategyready.go,strategysink.go,strategywire.go"
	if strings.Join(got, ",") != want {
		t.Fatalf("files with strategy symbols or library imports:\n got %v\nwant %s\ndecide the new file here, in review", got, want)
	}
}
