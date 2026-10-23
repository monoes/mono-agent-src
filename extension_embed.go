// Package monoagent holds the files that ship inside the monoagentcli binary
// from outside the Go packages.
package monoagent

import "embed"

// ChromeExtension is the MonoAgent Bridge browser extension, the same folder
// the release zips. `monoagentcli extension install` unpacks it to
// ~/.monoagent/extension, where the browser loads it unpacked.
//
//go:embed chrome-extension
var ChromeExtension embed.FS
