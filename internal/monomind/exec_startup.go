package monomind

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// The claude start-up limit (--startup-timeout, #1754). monomind's watchdog and the flag came in together, and a
// monomind from before them ignores the flag (its parser accepts unknown ones), so it needs no capability check.

// DefaultStartupTimeout is how long a claude turn may take to initialize
// (its SessionStart hooks and MCP servers) before monomind gives up. monomind's
// own fixed 30 s fails turns on a busy machine or a heavy hook setup.
const DefaultStartupTimeout = 120 * time.Second

// MaxStartupTimeout is the longest limit handed to monomind: a longer one is cut to it (its timer overflows
// above about 596h).
const MaxStartupTimeout = time.Hour

// StartupTimeoutEnv raises or lowers the limit: a Go duration such as "3m".
const StartupTimeoutEnv = "MONOAGENT_STARTUP_TIMEOUT"

// Where the effective start-up limit comes from.
const (
	StartupTimeoutFromDefault = "default"
	StartupTimeoutFromSetting = "set"
	StartupTimeoutFromEnv     = "env"
)

// ParseStartupTimeout reads a start-up limit written as a Go duration ("3m", "90s"; "0" is none), cut to
// MaxStartupTimeout. Anything else is an error: "3 min" or a bare number is never guessed at.
func ParseStartupTimeout(v string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, errors.New("a start-up limit cannot be negative")
	}
	return min(d, MaxStartupTimeout), nil
}

// startupWarnOut takes the warning about an override that is not used as written; startupWarned holds the
// values already warned about.
var (
	startupWarnOut io.Writer = os.Stderr
	startupWarned  sync.Map
)

// startupTimeoutOverride is the environment's limit, 0 when it sets none or one that cannot be read.
func startupTimeoutOverride() time.Duration {
	raw := os.Getenv(StartupTimeoutEnv)
	if strings.TrimSpace(raw) == "" {
		return 0
	}
	d, err := ParseStartupTimeout(raw)
	problem := ""
	if err != nil {
		problem = "is ignored: write a duration such as 3m or 180s"
	} else if asked, _ := time.ParseDuration(strings.TrimSpace(raw)); asked > d {
		problem = "is above the maximum: using " + d.String()
	}
	if problem != "" {
		if _, said := startupWarned.LoadOrStore(raw, true); !said {
			fmt.Fprintf(startupWarnOut, "warning: %s=%q %s\n", StartupTimeoutEnv, raw, problem)
		}
	}
	return d
}

// StartupTimeoutSetting is the limit for a saved value (zero: none saved) and where it comes from: the
// environment override, else the saved value, else DefaultStartupTimeout.
func StartupTimeoutSetting(saved time.Duration) (time.Duration, string) {
	if d := startupTimeoutOverride(); d > 0 {
		return d, StartupTimeoutFromEnv
	}
	if saved > 0 {
		return min(saved, MaxStartupTimeout), StartupTimeoutFromSetting
	}
	return DefaultStartupTimeout, StartupTimeoutFromDefault
}

// withStartupHint adds to monomind's start-up failure which setting raises the limit. monomind reports it as a
// non-fatal error before a nonzero exit.
func withStartupHint(msg string) string {
	if !strings.Contains(msg, "did not initialize") {
		return msg
	}
	return msg + " — set " + StartupTimeoutEnv + " (e.g. 180s), or for coder turns `coder set --startup-timeout 180s` " +
		"(in the app: Settings, Coder mode, Start-up limit), to give claude longer to start"
}

func startupTimeoutFor(opts ExecOptions) time.Duration {
	if opts.StartupTimeout > 0 {
		return min(opts.StartupTimeout, MaxStartupTimeout)
	}
	d, _ := StartupTimeoutSetting(0)
	return d
}
