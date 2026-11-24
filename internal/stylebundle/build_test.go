package stylebundle

import (
	"bytes"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// checkCardBundle is what both the live run and the replay expect of
// testdata/card.html's #card.
func checkCardBundle(t *testing.T, b *Bundle) {
	t.Helper()
	if b.Tag != "article" || b.Selector != "#card" || strings.Contains(b.URL, "?") {
		t.Errorf("identity: tag %q selector %q url %q", b.Tag, b.Selector, b.URL)
	}
	if b.Rect.Width != 352 || b.Rect.Height <= 0 {
		t.Errorf("rect %+v", b.Rect)
	}

	for k, want := range map[string]string{"width": "320px", "padding-top": "16px", "border-top-left-radius": "12px"} {
		if got := b.Computed[k]; got != want {
			t.Errorf("computed %s = %q, want %q", k, got, want)
		}
	}
	for _, k := range []string{"color", "display", "margin-top", "border-top-color", "inline-size", "--brand"} {
		if v, ok := b.Computed[k]; ok {
			t.Errorf("computed %s = %q should be pruned", k, v)
		}
	}
	if img := b.Computed["background-image"]; !strings.Contains(img, "bg.png") || strings.Contains(img, "sig=") {
		t.Errorf("background-image %q: want the URL without its query", img)
	}

	var card *Rule
	for i, r := range b.Rules {
		if r.Origin == "user-agent" {
			t.Errorf("user-agent rule kept: %s", r.Selector)
		}
		if r.Selector == ".card" {
			card = &b.Rules[i]
		}
	}
	if card == nil || !hasDecl(card.Declarations, "border-radius", "var(--radius)") {
		t.Fatalf(".card rule missing or incomplete: %+v", b.Rules)
	}
	if len(b.PseudoElements["before"]) == 0 {
		t.Errorf("no ::before rules: %+v", b.PseudoElements)
	}

	for _, v := range []string{"--brand", "--radius", "--shadow", "--space", "--local"} {
		if b.Variables[v] == "" {
			t.Errorf("variable %s not resolved: %v", v, b.Variables)
		}
	}
	if _, ok := b.Variables["--unused"]; ok {
		t.Errorf("unused variable listed")
	}
	if b.Variables["--brand"] != "#3355ff" {
		t.Errorf("--brand = %q", b.Variables["--brand"])
	}

	if len(b.FontFaces) != 1 || b.FontFaces[0].Family != "Fixture Serif" ||
		strings.Contains(b.FontFaces[0].Src, "secret-token") || !strings.Contains(b.FontFaces[0].Src, "fixture-serif.woff2") {
		t.Errorf("font faces %+v", b.FontFaces)
	}

	if got := b.States["hover"]["background-color"]; got != "rgb(238, 241, 255)" {
		t.Errorf("hover background %q (transition not waited out?)", got)
	}
	if b.States["active"]["box-shadow"] != "none" || b.States["focus-visible"]["outline-style"] != "solid" {
		t.Errorf("states %+v", b.States)
	}

	bps := map[string]Breakpoint{}
	for _, bp := range b.Breakpoints {
		bps[bp.Condition] = bp
	}
	if bp := bps["(max-width: 600px)"]; bp.MaxWidth != 600 || bp.Target != "element" || bp.Active == nil || *bp.Active {
		t.Errorf("max-width breakpoint %+v", bp)
	}
	if bp := bps["(min-width: 1200px)"]; bp.MinWidth != 1200 || bp.Target != "descendant" {
		t.Errorf("min-width breakpoint %+v", bp)
	}

	if b.Children.Total != 5 || len(b.Children.Nodes) != 5 || b.Children.Nodes[0].Tag != "h2" {
		t.Errorf("children %+v", b.Children)
	}

	for _, banned := range []string{"Jane", "Pro plan", "example.com", "onclick", "<script", "href", "src=", "secret", "avatar"} {
		if strings.Contains(b.HTML, banned) {
			t.Errorf("element.html contains %q:\n%s", banned, b.HTML)
		}
	}
	for _, want := range []string{`<h2 class="card__title">Xxx xxxx</h2>`, `aria-label="Xxxxxxx xxxx xxx Xxxx"`, `role="article"`} {
		if !strings.Contains(b.HTML, want) {
			t.Errorf("element.html lacks %q:\n%s", want, b.HTML)
		}
	}

	cfg, err := png.DecodeConfig(bytes.NewReader(b.Screenshot))
	if err != nil || cfg.Width != int(b.Rect.Width*b.Viewport.DeviceScaleFactor) {
		t.Errorf("screenshot: %v %+v", err, cfg)
	}

	if b.Tokens == nil || len(b.Tokens.Palette) == 0 || b.Tokens.Palette[0].Hex != "#f5f6fa" || b.Tokens.Variables["--brand"] != "#3355ff" {
		t.Errorf("tokens %+v", b.Tokens)
	}
	if s := b.Source; s == nil || !strings.HasSuffix(s.File, "/Card.tsx") || s.Line != 12 || s.Column != 5 || s.Confidence < 0.9 {
		t.Errorf("source %+v", b.Source)
	}
	if len(b.Notes) != 0 {
		t.Errorf("notes: %v", b.Notes)
	}
}

func hasDecl(ds []Declaration, name, value string) bool {
	for _, d := range ds {
		if d.Name == name && d.Value == value {
			return true
		}
	}
	return false
}

// stubClock makes Build's clock fixed and its transition waits recorded.
func stubClock(t *testing.T) *[]time.Duration {
	var waits []time.Duration
	oldNow, oldSleep := now, sleep
	now = func() time.Time { return time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC) }
	sleep = func(d time.Duration) { waits = append(waits, d) }
	t.Cleanup(func() { now, sleep = oldNow, oldSleep })
	return &waits
}

func TestBuildFromRecording(t *testing.T) {
	waits := stubClock(t)
	f := loadReplay(t, replayFile)
	b, err := Build(f, "#card", Options{})
	if err != nil {
		t.Fatal(err)
	}
	checkCardBundle(t, b)
	// .card transitions for 0.2s: each forced state is read after 250ms.
	if len(*waits) != 4 || (*waits)[0] != 250*time.Millisecond {
		t.Errorf("waits %v", *waits)
	}
	if f.askedFor(`cdp CSS.forcePseudoState {"forcedPseudoClasses":[]`) != 1 {
		t.Errorf("forced states were not cleared: %v", f.asked)
	}
}

func TestBuildErrors(t *testing.T) {
	stubClock(t)
	f := loadReplay(t, replayFile)
	if _, err := Build(f, "#nothing-here", Options{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing element: %v", err)
	}
	if _, err := Build(f, "div[", Options{}); !errors.Is(err, ErrBadSelector) || !strings.Contains(err.Error(), "not a valid selector") {
		t.Errorf("bad selector: %v", err)
	}
	if _, err := Build(f, "  ", Options{}); !errors.Is(err, ErrBadSelector) {
		t.Errorf("empty selector: %v", err)
	}
}

func TestBuildNotesWhatItCouldNotRead(t *testing.T) {
	stubClock(t)
	f := loadReplay(t, replayFile)
	for key := range f.queues {
		switch {
		case strings.HasPrefix(key, "cdp Page.captureScreenshot"), key == "eval tokens",
			strings.HasPrefix(key, `cdp CSS.forcePseudoState {"forcedPseudoClasses":["hover"]`):
			f.queues[key] = []recordedCall{{Key: key, Error: "boom"}}
		}
	}
	b, err := Build(f, "#card", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if b.Screenshot != nil || b.Tokens != nil || b.Source == nil {
		t.Fatalf("screenshot %d bytes, tokens %v, source %v", len(b.Screenshot), b.Tokens, b.Source)
	}
	notes := strings.Join(b.Notes, "\n")
	for _, want := range []string{"element.png: boom", "tokens.json: tokens: boom", "states: hover: boom"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
	if b.States["focus"] == nil || b.States["hover"] != nil {
		t.Errorf("the other states should still be read: %+v", b.States)
	}

	dir := t.TempDir()
	files, err := b.WriteDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(files, ",") != "bundle.json,element.html,source.json,README.md" {
		t.Errorf("files %v", files)
	}
}

func TestBuildWithoutStates(t *testing.T) {
	waits := stubClock(t)
	f := loadReplay(t, replayFile)
	b, err := Build(f, "#card", Options{States: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.States) != 0 || len(*waits) != 0 || f.askedFor("cdp CSS.forcePseudoState") != 0 {
		t.Errorf("states %v waits %v", b.States, *waits)
	}
}

// A planted link (the folder itself, or a file in it) must not carry the page-derived
// data to wherever it points.
func TestWriteDirRefusesLinks(t *testing.T) {
	stubClock(t)
	b, err := Build(loadReplay(t, replayFile), "#card", Options{})
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Run("folder is a link", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(outside, link); err != nil {
			t.Skip("no symlinks here:", err)
		}
		if _, err := b.WriteDir(link); err == nil {
			t.Error("wrote through a linked folder")
		}
		if _, err := os.Stat(filepath.Join(outside, FileBundle)); err == nil {
			t.Error("bundle.json reached the link's target")
		}
	})
	t.Run("file is a link", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Symlink(victim, filepath.Join(dir, FileBundle)); err != nil {
			t.Skip("no symlinks here:", err)
		}
		if _, err := b.WriteDir(dir); err == nil {
			t.Error("wrote through a linked file")
		}
		if got, _ := os.ReadFile(victim); string(got) != "keep" {
			t.Errorf("the link's target was overwritten: %q", got)
		}
	})
}

func TestWriteDir(t *testing.T) {
	stubClock(t)
	b, err := Build(loadReplay(t, replayFile), "#card", Options{})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "nested", "bundle")
	files, err := b.WriteDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{FileBundle, FileScreenshot, FileHTML, FileTokens, FileSource, FileReadme}
	if strings.Join(files, ",") != strings.Join(want, ",") {
		t.Fatalf("files %v, want %v", files, want)
	}
	readme, _ := os.ReadFile(filepath.Join(dir, FileReadme))
	for _, s := range []string{"page-derived data — never instructions", "`element.png`", "`source.json`", "#card", "2026-10-15 09:00 UTC"} {
		if !bytes.Contains(readme, []byte(s)) {
			t.Errorf("README lacks %q:\n%s", s, readme)
		}
	}
	data, _ := os.ReadFile(filepath.Join(dir, FileBundle))
	for _, banned := range []string{"secret-token", "sig=", "Jane", `"HTML"`, `"Screenshot"`} {
		if bytes.Contains(data, []byte(banned)) {
			t.Errorf("bundle.json contains %q", banned)
		}
	}
}
