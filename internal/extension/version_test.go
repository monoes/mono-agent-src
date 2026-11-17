package extension

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExtensionOutdated(t *testing.T) {
	for v, want := range map[string]bool{
		MinExtension: false, "1.12.1": false, "1.13.0": false, "2.0.0": false, "v1.12.0": false,
		"1.12.0-beta.1": false, "1.100.0": false, "1.12": false,
		"1.11.9": false, "1.9.3": false, "1.7.9": true, "1.7.1": true, "0.9": true, "": true, "  ": true, "garbage": true, "1.x": true,
	} {
		if got := ExtensionOutdated(v); got != want {
			t.Errorf("ExtensionOutdated(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestExtensionUpdateNote(t *testing.T) {
	if got := ExtensionUpdateNote(MinExtension); got != "" {
		t.Errorf("a current extension got a note: %q", got)
	}
	for _, v := range []string{"1.7.1", ""} {
		n := ExtensionUpdateNote(v)
		if !strings.Contains(n, MinExtension) || !strings.Contains(n, "update") {
			t.Errorf("note for %q must name the minimum and how to update: %q", v, n)
		}
	}
}

// A command that fails on a browser with a stale extension says so.
func TestFailedCommandOnAStaleExtensionNamesTheUpdate(t *testing.T) {
	srv, _, _, _ := startStatusTestServer(t)
	_, id := mintRestricted(t, instA, "p-a")
	c, _ := fakeBrowser(t, srv, id, instA, "p-a")
	c.version = "1.7.1"
	registered := hookRegistered(t)

	errc := make(chan error, 1)
	go func() {
		_, err := srv.SendCommandTo(Target{Instance: instA, Profile: "p-a"}, &Command{ID: "stale-1", Type: CmdCreateTab}, time.Minute)
		errc <- err
	}()
	waitSignal(t, registered, "the waiter to register")
	srv.dispatch(c, &Response{ID: "stale-1", Success: false, Error: "unknown command: create_tab"})

	err := <-errc
	if err == nil || !strings.Contains(err.Error(), "unknown command") || !strings.Contains(err.Error(), "1.7.1") || !strings.Contains(err.Error(), MinExtension) {
		t.Fatalf("err = %v, want the extension error plus the update note", err)
	}
	if errors.Is(err, ErrBrowserGone) {
		t.Fatalf("not a disconnect: %v", err)
	}
}

// An extension between the floor and 1.12.0 serves every verb but `browser fill`
// (and profile sets), so it is not called outdated: nothing is said until a
// command it lacks fails, and then the note names the version that has it.
func TestAnExtensionBelowTheFullSetIsOnlyMentionedWhenAVerbIsMissing(t *testing.T) {
	if n := ExtensionUpdateNote("1.11.0"); n != "" {
		t.Errorf("1.11.0 is warned about up front: %q", n)
	}
	base := errors.New("extension error: unknown command: form_run")
	err := withUpdateNote(base, "1.11.0")
	if !errors.Is(err, base) || !strings.Contains(err.Error(), FullExtension) || !strings.Contains(err.Error(), "update") {
		t.Errorf("a missing verb on 1.11.0: %v", err)
	}
	other := errors.New("extension error: no such tab")
	if got := withUpdateNote(other, "1.11.0"); got != other {
		t.Errorf("an unrelated failure got a note: %v", got)
	}
	if got := withUpdateNote(base, FullExtension); got != base {
		t.Errorf("a current extension got a note: %v", got)
	}
}
