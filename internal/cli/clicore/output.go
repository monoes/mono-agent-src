package clicore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"

	"github.com/monoes/mono-agent/internal/automation"
)

// WriteJSONTo writes v as indented JSON to w.
func WriteJSONTo(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// PrintJSON writes v to stdout as indented JSON. Small shared helper for the
// commands that honor the global --json flag.
func PrintJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// TruncateStr shortens s to at most n characters (runes), appending "..." if truncated.
func TruncateStr(s string, n int) string {
	if n < 0 {
		n = 0
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 3 {
		return string(r[:n])
	}
	return string(r[:n-3]) + "..."
}

// WebURL parses raw and accepts only an http or https URL with a host: the pages the
// CLI may open in the user's logged-in browser. file:, javascript:, chrome: and the like
// are refused.
func WebURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported URL scheme %q — only http and https are allowed", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("invalid URL %q: no host", raw)
	}
	return u, nil
}

// userHomeDir is os.UserHomeDir; a test replaces it.
var userHomeDir = os.UserHomeDir

// ExpandPathE expands a leading ~ to the user's home directory. A path that
// needs the home directory when there is none is an error, not a literal "~/..."
// that the caller would then create under the working directory.
func ExpandPathE(path string) (string, error) { return expandPathFor(runtime.GOOS, path) }

func expandPathFor(goos, path string) (string, error) {
	rest, ok := strings.CutPrefix(path, "~/")
	if !ok && goos == "windows" {
		rest, ok = strings.CutPrefix(path, `~\`)
	}
	if path == "~" {
		rest, ok = "", true
	}
	if !ok {
		return path, nil
	}
	home, err := userHomeDir()
	if err != nil || home == "" {
		return "", errors.New("cannot resolve your home directory: set HOME (USERPROFILE on Windows) or pass an absolute --db-path")
	}
	return filepath.Join(home, rest), nil
}

// ExpandPath expands ~ to the user's home directory. Without a home directory
// it returns the path unchanged; a caller that creates files from the result
// uses ExpandPathE instead.
func ExpandPath(path string) string {
	if p, err := ExpandPathE(path); err == nil {
		return p
	}
	return path
}

// NewPlainTable builds a *tablewriter.Table matching this CLI's established
// look from before tablewriter v1: no box-drawing borders, no auto-wrapping
// of long cell content. v1 removed the old NewWriter+SetHeader+
// SetBorder(false)+SetAutoWrapText(false) setter-based API entirely in
// favor of this Rendition/Config builder shape (SetHeader/SetBorder/
// SetAutoWrapText/SetColumnAlignment and the ALIGN_* constants no longer
// exist on *tablewriter.Table at all -- verified directly against v1.1.4's
// source, not just its changelog).
//
// perColumnAlign is optional (nil for the default left-alignment
// everywhere, matching every call site that never called
// SetColumnAlignment) -- pass one tw.Align per header column to right-
// align a "Field"/"Value"-style table's first column, matching the four
// call sites that used SetColumnAlignment([]int{ALIGN_RIGHT, ALIGN_LEFT}).
func NewPlainTable(w io.Writer, headers []string, perColumnAlign []tw.Align) *tablewriter.Table {
	cellFmt := tw.CellFormatting{AutoWrap: tw.WrapNone}
	// v1's own default row alignment is centered (confirmed directly by
	// rendering a table with no alignment override) -- v0.0.5's default was
	// left-aligned, which every call site with no SetColumnAlignment call
	// implicitly relied on. Restore that default explicitly here; only
	// override it with per-column alignment when the caller asks for it.
	rowCfg := tw.CellConfig{Formatting: cellFmt, Alignment: tw.CellAlignment{Global: tw.AlignLeft}}
	if len(perColumnAlign) > 0 {
		rowCfg.Alignment = tw.CellAlignment{PerColumn: perColumnAlign}
	}
	table := tablewriter.NewTable(w,
		tablewriter.WithRenderer(renderer.NewBlueprint(tw.Rendition{Borders: tw.BorderNone})),
		tablewriter.WithConfig(tablewriter.Config{
			Header: tw.CellConfig{Formatting: cellFmt},
			Row:    rowCfg,
		}),
	)
	table.Header(headers)
	return table
}

// OpenAutomationRegistry opens the default registry and folds any new
// legacy ~/.monoagent/actions/<p> directories into local-<p> packages.
// Every automation subcommand starts here. Nothing is seeded: the official
// packages come from monoes.me (`monoagentcli library install automation
// <id>`), and packages installed by earlier versions stay as they are.
// version is the CLI's release version ("" in a dev build).
func OpenAutomationRegistry(version string) (*automation.Registry, error) {
	if version != "" { // release build: enforce manifests' "engine" ranges
		automation.EngineVersion = version
	}
	reg, err := automation.Default()
	if err != nil {
		return nil, fmt.Errorf("open automation registry: %w", err)
	}
	rep, err := reg.Boot()
	if err != nil {
		return nil, fmt.Errorf("fold legacy actions: %w", err)
	}
	for _, id := range rep.LegacyWrapped {
		fmt.Fprintf(os.Stderr, "note: wrapped legacy actions into automation package %s (~/.monoagent/actions is left as is)\n", id)
	}
	return reg, nil
}
