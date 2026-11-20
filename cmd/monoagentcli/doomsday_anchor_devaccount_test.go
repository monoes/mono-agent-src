//go:build devaccount

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/testhome"
)

func anchorSeamWiring() *doomsdaycli.Wiring {
	return &doomsdaycli.Wiring{Witness: &doomsdaycli.WitnessSetup{Secrets: stop.OSSecretStore{}}}
}

func TestAnchorSeamOffKeepsKeychain(t *testing.T) {
	t.Setenv("MONOAGENT_DEV_ANCHOR_FILE_STORE", "")
	w := anchorSeamWiring()
	devAnchorSeam(w)
	if _, ok := w.Witness.Secrets.(stop.OSSecretStore); !ok {
		t.Fatalf("seam off changed the store to %T", w.Witness.Secrets)
	}
}

func TestAnchorSeamOnUsesFileStoreInHome(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv("MONOAGENT_DEV_ANCHOR_FILE_STORE", "1")
	w := anchorSeamWiring()
	devAnchorSeam(w)
	fs, ok := w.Witness.Secrets.(*stop.FileSecretStore)
	if !ok {
		t.Fatalf("seam on gave %T, want a file store", w.Witness.Secrets)
	}
	if filepath.Dir(fs.Dir) != home {
		t.Fatalf("store dir %q is not directly inside HOME %q", fs.Dir, home)
	}
	if err := fs.Set("k", "v"); err != nil {
		t.Fatal(err)
	}
	if v, err := fs.Get("k"); err != nil || v != "v" {
		t.Fatalf("roundtrip: %q %v", v, err)
	}
}

// Switched on but unusable: never the keychain, and never a silent degrade.
func TestAnchorSeamOnUnusableRefusesLoudly(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".dev-anchor-secrets"), nil, 0o600); err != nil { // a file where the dir must go
		t.Fatal(err)
	}
	testhome.SetHome(t, home)
	t.Setenv("MONOAGENT_DEV_ANCHOR_FILE_STORE", "1")
	w := anchorSeamWiring()
	var warned string
	w.Witness.Warn = func(m string) { warned = m }
	devAnchorSeam(w)
	if _, ok := w.Witness.Secrets.(stop.OSSecretStore); ok {
		t.Fatal("fell back to the keychain")
	}
	if warned == "" {
		t.Fatal("no warning that the file store is unusable")
	}
}

// Set to anything but "1" (true, yes): the switch is not understood, so the anchor gets no store (never the keychain, never a
// silent default) and the warning names the variable and the value it takes.
func TestAnchorSeamOtherValueSelectsNoStoreAndWarns(t *testing.T) {
	for _, v := range []string{"true", "yes"} {
		t.Run(v, func(t *testing.T) {
			testhome.SetHome(t, t.TempDir())
			t.Setenv("MONOAGENT_DEV_ANCHOR_FILE_STORE", v)
			w := anchorSeamWiring()
			var warned string
			w.Witness.Warn = func(m string) { warned = m }
			devAnchorSeam(w)
			if _, ok := w.Witness.Secrets.(stop.NoSecretStore); !ok {
				t.Fatalf("%s=%s gave %T, want no secret store", "MONOAGENT_DEV_ANCHOR_FILE_STORE", v, w.Witness.Secrets)
			}
			if !strings.Contains(warned, "MONOAGENT_DEV_ANCHOR_FILE_STORE=1") {
				t.Fatalf("the warning does not name the variable and its accepted value: %q", warned)
			}
		})
	}
}
