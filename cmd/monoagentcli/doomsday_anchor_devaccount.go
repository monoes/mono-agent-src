//go:build devaccount

package main

import (
	"os"
	"path/filepath"

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

func devAnchorSeam(w *doomsdaycli.Wiring) {
	if os.Getenv(devAnchorEnv) != "1" || w.Witness == nil {
		return
	}
	home := os.Getenv("HOME")
	dir := filepath.Join(home, ".dev-anchor-secrets")
	err := os.ErrInvalid
	if home != "" {
		err = os.MkdirAll(dir, 0o700)
	}
	if err != nil {
		if w.Witness.Warn != nil {
			w.Witness.Warn(devAnchorEnv + " is set but the file store cannot be used (" + err.Error() + "): the anchor has no store")
		}
		w.Witness.Secrets = stop.NoSecretStore{}
		return
	}
	w.Witness.Secrets = &stop.FileSecretStore{Dir: dir}
}
