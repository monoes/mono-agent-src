package chat

import (
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/monomind"
)

// `coder set --startup-timeout`: how long claude may take to initialize in a coder turn, the lead's and its
// workers' (#1754).

// parseStartupTimeout is the flag's value as stored: "" (the default again) for 0.
func parseStartupTimeout(v string) (string, error) {
	d, err := parseDurationFlag(v)
	if strings.TrimSpace(v) == "" || err != nil || d != 0 && d < time.Second {
		return "", clicore.InvalidInput("--startup-timeout must be a duration of at least 1s (e.g. 3m), or 0 for the default")
	}
	if d == 0 {
		return "", nil
	}
	return d.String(), nil
}

// resolveStartupTimeout is the limit a coder turn runs under and where it comes from: the environment
// override, else the stored setting (an unreadable one counts as none), else the default.
func (s coderSettings) resolveStartupTimeout() (time.Duration, string) {
	saved, err := parseDurationFlag(s.StartupTimeout)
	if err != nil {
		saved = 0
	}
	return monomind.StartupTimeoutSetting(saved)
}

// startupTimeoutText is the status line's value.
func startupTimeoutText(st coderStatus) string {
	switch st.StartupTimeoutSource {
	case monomind.StartupTimeoutFromEnv:
		return st.StartupTimeoutEffective + " (from " + monomind.StartupTimeoutEnv + ")"
	case monomind.StartupTimeoutFromSetting:
		return st.StartupTimeoutEffective + " (set by `coder set --startup-timeout`)"
	}
	return st.StartupTimeoutEffective + " (default)"
}
