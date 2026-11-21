package extension

import (
	"fmt"
	"strconv"
	"strings"
)

// MinExtension is the oldest extension the CLI warns nothing about. An older
// one still connects (nothing is refused) but fails verb by verb, so the CLI
// warns up front and says how to update it.
//
// 1.8.0 is the floor because every verb in protocol.go except one is in 1.8.0
// or older. The exception is form_run (`browser fill`, commit 295c7d6e3), first
// shipped by 1.12.0 (bdd13d1b9, which also brought profile sets, see
// SupportsProfileSets and FullExtension). A 1.8-1.11 extension is therefore not
// called outdated: it works, and is only mentioned when a command it lacks
// fails (withUpdateNote).
const MinExtension = "1.8.0"

// FullExtension is the first extension that serves every verb and profile sets.
const FullExtension = "1.12.0"

// parseVersion reads "1.12.0", "v1.12", "1.12.0-beta.1" as numeric parts.
// ok is false for a blank or non-numeric version.
func parseVersion(v string) (parts [3]int, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return parts, false
	}
	fields := strings.Split(v, ".")
	if len(fields) > 3 {
		return parts, false
	}
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return parts, false
		}
		parts[i] = n
	}
	return parts, true
}

// ExtensionOutdated reports whether an extension of this version is older
// than MinExtension. A blank or unreadable version (extensions before 1.5
// send none) counts as outdated.
func ExtensionOutdated(version string) bool { return olderThan(version, MinExtension) }

// olderThan reports whether version is older than floor; an unreadable version is older.
func olderThan(version, floor string) bool {
	got, ok := parseVersion(version)
	if !ok {
		return true
	}
	min, _ := parseVersion(floor)
	for i := range got {
		if got[i] != min[i] {
			return got[i] < min[i]
		}
	}
	return false
}

// ExtensionUpdateNote is the warning for a browser whose extension is older
// than MinExtension, or "" when it is current.
func ExtensionUpdateNote(version string) string {
	if !ExtensionOutdated(version) {
		return ""
	}
	have := "an unknown version"
	if v := strings.TrimSpace(version); v != "" {
		have = v
	}
	return fmt.Sprintf("its MonoAgent Bridge extension is %s, older than the %s this CLI expects: update the extension "+
		"(reload it at chrome://extensions, or reinstall it from `monoagentcli extension install`)", have, MinExtension)
}

// withUpdateNote appends the update warning to err when the browser's
// extension is older than MinExtension, and also, for an extension older than
// FullExtension, when the failure is a command it does not know (the extension
// throws "Unknown command type: x", the older route "unknown command: x"); the error
// stays unwrapped-compatible.
func withUpdateNote(err error, version string) error {
	if note := ExtensionUpdateNote(version); note != "" {
		return fmt.Errorf("%w (%s)", err, note)
	}
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unknown command") && olderThan(version, FullExtension) {
		return fmt.Errorf("%w (its MonoAgent Bridge extension is %s and lacks this command; %s has it: update the extension "+
			"(reload it at chrome://extensions, or reinstall it from `monoagentcli extension install`))", err, strings.TrimSpace(version), FullExtension)
	}
	return err
}
