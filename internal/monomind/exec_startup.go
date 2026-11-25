package monomind

import (
	"os"
	"time"
)

// The claude start-up limit (--startup-timeout, #1754). monomind's watchdog and the flag came in together, and a
// monomind from before them ignores the flag (its parser accepts unknown ones), so it needs no capability check.

// DefaultStartupTimeout is how long a claude turn may take to initialize
// (its SessionStart hooks and MCP servers) before monomind gives up. monomind's
// own fixed 30 s fails turns on a busy machine or a heavy hook setup.
const DefaultStartupTimeout = 120 * time.Second

// StartupTimeoutEnv raises or lowers the limit: a Go duration such as "3m".
const StartupTimeoutEnv = "MONOAGENT_STARTUP_TIMEOUT"

// Where the effective start-up limit comes from.
const (
	StartupTimeoutFromDefault = "default"
	StartupTimeoutFromSetting = "set"
	StartupTimeoutFromEnv     = "env"
)

// StartupTimeoutSetting is the limit for a saved value (zero: none saved) and where it comes from: the
// environment override, else the saved value, else DefaultStartupTimeout.
func StartupTimeoutSetting(saved time.Duration) (time.Duration, string) {
	if d, err := time.ParseDuration(os.Getenv(StartupTimeoutEnv)); err == nil && d > 0 {
		return d, StartupTimeoutFromEnv
	}
	if saved > 0 {
		return saved, StartupTimeoutFromSetting
	}
	return DefaultStartupTimeout, StartupTimeoutFromDefault
}

func startupTimeoutFor(opts ExecOptions) time.Duration {
	if opts.StartupTimeout > 0 {
		return opts.StartupTimeout
	}
	d, _ := StartupTimeoutSetting(0)
	return d
}
