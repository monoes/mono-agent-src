//go:build devaccount

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

// THE DEVELOPMENT SEAM FOR THE WITNESS ANCHOR (tag devaccount). On macOS the anchor lives in the real login keychain, so a
// real-host test (kill the host, restart, stop) would write to the developer's keychain. With MONOAGENT_DEV_ANCHOR_FILE_STORE=1
// the anchor is kept in a directory of the (scratch) HOME instead. No shipped build carries this file; nothing here is
// reachable without the tag. Switched on but unusable, the anchor has no store at all (never the keychain): the run is then
// `anchor-bound: no` and a warning says why.
const devAnchorEnv = "MONOAGENT_DEV_ANCHOR_FILE_STORE"

func init() {
	prev := doomsdayWiringHook
	doomsdayWiringHook = func(w *doomsdaycli.Wiring) {
		if prev != nil {
			prev(w)
		}
		devAnchorSeam(w)
	}
}

// devAnchorSeam: unset or empty leaves the store as it is; exactly "1" selects the file store; any other value is a switch that
// was meant to be on but is not understood, so the anchor gets no store (never the keychain) and a warning says which value works.
func devAnchorSeam(w *doomsdaycli.Wiring) {
	v := os.Getenv(devAnchorEnv)
	if v == "" || w.Witness == nil {
		return
	}
	if v != "1" {
		devAnchorNoStore(w, devAnchorEnv+"="+strconv.Quote(v)+" is not understood (set "+devAnchorEnv+"=1 for the file store, or unset it): the anchor has no store")
		return
	}
	home := os.Getenv("HOME")
	dir := filepath.Join(home, ".dev-anchor-secrets")
	err := os.ErrInvalid
	if home != "" {
		err = os.MkdirAll(dir, 0o700)
	}
	if err != nil {
		devAnchorNoStore(w, devAnchorEnv+" is set but the file store cannot be used ("+err.Error()+"): the anchor has no store")
		return
	}
	w.Witness.Secrets = &stop.FileSecretStore{Dir: dir}
}

func devAnchorNoStore(w *doomsdaycli.Wiring, warning string) {
	if w.Witness.Warn != nil {
		w.Witness.Warn(warning)
	} else {
		fmt.Fprintln(os.Stderr, "doomsday: "+warning)
	}
	w.Witness.Secrets = stop.NoSecretStore{}
}
