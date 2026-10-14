package connections

import (
	"fmt"
	"sort"
	"sync"
)

// Platforms registered at run time (API packages, internal/apipkg) live
// beside the built-in Registry. Get, All and ByConnectVia — and
// so every connect flow (api key, OAuth PKCE), `connect` listing and
// Store.GetOrResolve's token refresh — see both. A built-in platform always
// wins: RegisterPlatform refuses its id.
var (
	dynMu      sync.RWMutex
	dynamic    = map[string]PlatformDef{}
	srcMu      sync.Mutex
	srcPending []func()
)

// IsBuiltin reports whether id is a built-in platform.
func IsBuiltin(id string) bool {
	_, ok := Registry[id]
	return ok
}

// RegisterPlatform adds (or replaces) a run-time platform. It refuses an
// empty id, a built-in id, and a definition without methods.
func RegisterPlatform(def PlatformDef) error {
	if def.ID == "" {
		return fmt.Errorf("connections: RegisterPlatform: empty platform id")
	}
	if IsBuiltin(def.ID) {
		return fmt.Errorf("connections: RegisterPlatform: %q is a built-in platform", def.ID)
	}
	if len(def.Methods) == 0 {
		return fmt.Errorf("connections: RegisterPlatform %q: no auth methods", def.ID)
	}
	if def.Fields == nil {
		def.Fields = map[AuthMethod][]CredentialField{}
	}
	dynMu.Lock()
	dynamic[def.ID] = def
	dynMu.Unlock()
	return nil
}

// UnregisterPlatform removes a run-time platform (built-ins are untouched).
func UnregisterPlatform(id string) {
	dynMu.Lock()
	delete(dynamic, id)
	dynMu.Unlock()
}

// AddPlatformSource queues fn, which registers platforms, to run once before
// the next platform lookup. Packages that load lazily register this way so
// importing them costs nothing until a platform is needed.
func AddPlatformSource(fn func()) {
	srcMu.Lock()
	srcPending = append(srcPending, fn)
	srcMu.Unlock()
}

// loadSources runs the queued platform sources.
func loadSources() {
	srcMu.Lock()
	pending := srcPending
	srcPending = nil
	srcMu.Unlock()
	for _, fn := range pending {
		fn()
	}
}

func dynamicGet(id string) (PlatformDef, bool) {
	loadSources()
	dynMu.RLock()
	defer dynMu.RUnlock()
	p, ok := dynamic[id]
	return p, ok
}

// dynamicAll returns the run-time platforms sorted by id.
func dynamicAll() []PlatformDef {
	loadSources()
	dynMu.RLock()
	defer dynMu.RUnlock()
	out := make([]PlatformDef, 0, len(dynamic))
	for _, p := range dynamic {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
