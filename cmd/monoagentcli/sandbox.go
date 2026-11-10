package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/monomind"
)

// sandboxFallbackConfigName is the configs-table row (`config get|set`
// reach it too) holding the sandbox-fallback setting as plain text.
const sandboxFallbackConfigName = "sandbox.fallback"

// sandboxFallbackTTL bounds how stale a long-running process's view of the
// setting can be, so reading it per turn costs at most one query a second.
const sandboxFallbackTTL = time.Second

// loadSandboxFallback returns the saved value, "" when none is saved.
func loadSandboxFallback(db *sql.DB) (string, error) {
	var raw string
	err := db.QueryRow(`SELECT config_data FROM configs WHERE name = ?`, sandboxFallbackConfigName).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", sandboxFallbackConfigName, err)
	}
	// `config set sandbox.fallback '"fail"'` stores the quotes too.
	return strings.Trim(strings.TrimSpace(raw), `"'`), nil
}

func saveSandboxFallback(db *sql.DB, v string) error {
	_, err := db.Exec(`INSERT INTO configs (name, config_data, created_at, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(name) DO UPDATE SET config_data = excluded.config_data, updated_at = excluded.updated_at`,
		sandboxFallbackConfigName, v)
	if err != nil {
		return fmt.Errorf("saving %s: %w", sandboxFallbackConfigName, err)
	}
	return nil
}

// installSandboxFallbackProvider points monomind.SandboxFallbackProvider at
// the database cfg names, read on every call (cached for a second) so a
// daemon or bridge sees a change without a restart. It opens the file
// read-only and never creates it; anything unreadable means "run", the
// default.
func installSandboxFallbackProvider(cfg *globalConfig) {
	var mu sync.Mutex
	var cached string
	var at time.Time
	monomind.SandboxFallbackProvider = func() string {
		mu.Lock()
		defer mu.Unlock()
		if !at.IsZero() && time.Since(at) < sandboxFallbackTTL {
			return cached
		}
		// An unreadable or locked database keeps the last value read: a
		// user on "fail" must not drop to "run" for a second.
		if v, err := readSandboxFallbackFile(expandPath(cfg.DBPath)); err == nil {
			cached = v
		}
		at = time.Now()
		return cached
	}
}

// readSandboxFallbackFile reads the saved value straight from the database
// file, without migrations. A missing file is no setting ("", nil); a file
// that can't be read is an error.
func readSandboxFallbackFile(path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	db, err := sql.Open("sqlite", "file:"+(&url.URL{Path: path}).EscapedPath()+"?mode=ro&_pragma=busy_timeout(1000)")
	if err != nil {
		return "", err
	}
	defer db.Close()
	return loadSandboxFallback(db)
}

// sandboxFallbackReport is `sandbox fallback --json`. Capable is whether
// the installed monomind has agent-exec-sandbox-fallback; without it the
// setting is kept but not applied.
type sandboxFallbackReport struct {
	Setting         string   `json:"setting"`
	Value           string   `json:"value"`
	Source          string   `json:"source"` // env | saved | default
	Saved           string   `json:"saved,omitempty"`
	Default         string   `json:"default"`
	Values          []string `json:"values"`
	Env             string   `json:"env"`
	Capable         bool     `json:"capable"`
	MonomindVersion string   `json:"monomindVersion,omitempty"`
}

func newSandboxCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Settings for the sandbox agent turns run in",
	}
	cmd.AddCommand(newSandboxFallbackCmd(cfg))
	return cmd
}

func newSandboxFallbackCmd(cfg *globalConfig) *cobra.Command {
	var set string
	c := &cobra.Command{
		Use:   "fallback",
		Short: "Show or set what a turn does when its agent runtime has no sandbox for it",
		Long: "Every agent turn asks for a workspace-write sandbox. When the runtime has no such mode this setting decides:\n" +
			"  run        run the turn without a sandbox (default; what monoagentcli always did)\n" +
			"  strictest  use the strictest sandbox the runtime has (needs monomind with agent-exec-sandbox-fallback)\n" +
			"  fail       refuse the turn (needs the same)\n" +
			"A turn that requires a sandbox is refused under every value unless it got the one it asked for. " +
			monomind.SandboxFallbackEnv + " overrides the saved value.",
		Example: `  monoagentcli sandbox fallback --json
  monoagentcli sandbox fallback --set strictest`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			if cmd.Flags().Changed("set") {
				if err := cfg.requireOperator("set the sandbox fallback, what an agent turn does without a sandbox"); err != nil {
					return err
				}
				v, ok := monomind.ParseSandboxFallback(set)
				if !ok {
					return errInvalidInput("--set %q: must be one of %s", set, strings.Join(monomind.SandboxFallbackValues, ", "))
				}
				if err := saveSandboxFallback(db.DB, v); err != nil {
					return err
				}
			}
			saved, err := loadSandboxFallback(db.DB)
			if err != nil {
				return err
			}
			rep := newSandboxFallbackReport(cmd, saved)
			if cfg.JSONOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(rep)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "sandbox fallback: %s (%s)\n", rep.Value, rep.Source)
			if !rep.Capable {
				fmt.Fprintf(w, "monomind lacks %s: the setting is kept but turns behave as 'run'\n", monomind.CapAgentExecSandboxFallback)
			}
			return nil
		},
	}
	c.Flags().StringVar(&set, "set", "", "Set the value (operator only): "+strings.Join(monomind.SandboxFallbackValues, " | "))
	withJSONErrors(cfg, c)
	return c
}

func newSandboxFallbackReport(cmd *cobra.Command, saved string) sandboxFallbackReport {
	rep := sandboxFallbackReport{
		Setting: sandboxFallbackConfigName, Value: monomind.SandboxFallbackRun, Source: "default", Saved: saved,
		Default: monomind.SandboxFallbackRun, Values: monomind.SandboxFallbackValues, Env: monomind.SandboxFallbackEnv,
	}
	if v, ok := monomind.ParseSandboxFallback(saved); ok {
		rep.Value, rep.Source = v, "saved"
	}
	if v, ok := monomind.ParseSandboxFallback(os.Getenv(monomind.SandboxFallbackEnv)); ok {
		rep.Value, rep.Source = v, "env"
	}
	if caps, err := capabilityProbe(cmd); err == nil && caps != nil {
		rep.Capable, rep.MonomindVersion = caps.Has(monomind.CapAgentExecSandboxFallback), caps.Version
	}
	return rep
}
