package main

import "github.com/wailsapp/wails/v2/pkg/runtime"

// eventsEmit sends an event to the page. It is the Wails runtime's EventsEmit in every build; the stub-runtime browser end-to-end
// (build tag e2ehost, e2e_host.go) replaces it, because without a Wails window there is no runtime context to emit on.
var eventsEmit = runtime.EventsEmit
