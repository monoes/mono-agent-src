package chromium

import (
	"runtime"
	"testing"
)

// existsOnly reports true for every candidate path of the given ids.
func existsOnly(ids ...string) func(string) bool {
	set := map[string]bool{}
	for _, id := range ids {
		for _, p := range candidatePaths(id) {
			set[p] = true
		}
	}
	return func(p string) bool { return set[p] }
}

func TestDetect_InstalledFirst(t *testing.T) {
	if len(candidatePaths("brave")) == 0 || len(candidatePaths("chrome")) == 0 {
		t.Skipf("no candidate paths for chrome or brave on %s (no PATH?)", runtime.GOOS)
	}
	got := Detect(existsOnly("brave", "chrome"))
	if len(got) != 7 {
		t.Fatalf("len = %d, want 7: %+v", len(got), got)
	}
	if got[0].ID != "chrome" || !got[0].Installed || got[1].ID != "brave" || !got[1].Installed {
		t.Fatalf("first two = %+v, %+v; want chrome, brave installed", got[0], got[1])
	}
	wantRest := []string{"edge", "arc", "vivaldi", "opera", "chromium"}
	for i, id := range wantRest {
		b := got[i+2]
		if b.ID != id || b.Installed {
			t.Errorf("got[%d] = %+v, want %s not installed", i+2, b, id)
		}
	}
}

func TestKnown_ExtensionsURLs(t *testing.T) {
	want := []struct{ id, url, side string }{
		{"chrome", "chrome://extensions", "right"},
		{"edge", "edge://extensions", "left"},
		{"brave", "brave://extensions", "right"},
		{"arc", "arc://extensions", "right"},
		{"vivaldi", "vivaldi://extensions", "right"},
		{"opera", "opera://extensions", "right"},
		{"chromium", "chrome://extensions", "right"},
	}
	got := Known()
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		b := got[i]
		if b.ID != w.id || b.ExtensionsURL != w.url || b.DevModeSide != w.side || b.Installed || b.Name == "" {
			t.Errorf("Known()[%d] = %+v, want id %s url %s side %s, not installed, named", i, b, w.id, w.url, w.side)
		}
	}
}

func TestLaunchCommand_RefusesUnknownAndMissing(t *testing.T) {
	nothing := func(string) bool { return false }
	for _, id := range []string{"nope", "../x", "", "chrome"} {
		if name, args, err := launchCommand(id, nothing); err == nil {
			t.Errorf("launchCommand(%q) = %q %q, want an error", id, name, args)
		}
	}
	// The exported LaunchCommand refuses unknown ids without touching the disk.
	for _, id := range []string{"nope", "../x"} {
		if _, _, err := LaunchCommand(id); err == nil {
			t.Errorf("LaunchCommand(%q): want an error", id)
		}
	}
}

func TestLaunchCommand_UsesTheDetectedPath(t *testing.T) {
	paths := candidatePaths("brave")
	if len(paths) == 0 {
		t.Skipf("no candidate paths for brave on %s", runtime.GOOS)
	}
	found := paths[len(paths)-1]
	name, args, err := launchCommand("brave", func(p string) bool { return p == found })
	if err != nil {
		t.Fatal(err)
	}
	wantName, wantArgs := launchArgs(found)
	if name != wantName || len(args) != len(wantArgs) {
		t.Fatalf("launchCommand = %q %q, want %q %q", name, args, wantName, wantArgs)
	}
	for i := range args {
		if args[i] != wantArgs[i] {
			t.Fatalf("launchCommand = %q %q, want %q %q", name, args, wantName, wantArgs)
		}
	}
	if runtime.GOOS == "darwin" && (name != "open" || len(args) != 2 || args[0] != "-a" || args[1] != found) {
		t.Errorf("macOS launch = %q %q, want open -a %s", name, args, found)
	}
}
