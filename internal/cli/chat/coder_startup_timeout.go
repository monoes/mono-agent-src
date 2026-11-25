package chat

import (
	"time"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/monomind"
)

// `coder set --startup-timeout`: how long claude may take to initialize in a coder turn, the lead's and its
// workers' (#1754).

// parseStartupTimeout is the flag's value as stored: "" (the default again) for 0. A value above
// monomind.MaxStartupTimeout is stored as that.
func parseStartupTimeout(v string) (string, error) {
	d, err := monomind.ParseStartupTimeout(v)
	if err != nil || d != 0 && d < time.Second {
		return "", clicore.InvalidInput("--startup-timeout must be a duration of at least 1s with its unit (e.g. 3m or 180s), or 0 for the default")
	}
	if d == 0 {
		return "", nil
	}
	return d.String(), nil
}

// resolveStartupTimeout is the limit a coder turn runs under and where it comes from: the environment
// override, else the stored setting (an unreadable one counts as none), else the default.
func (s coderSettings) resolveStartupTimeout() (time.Duration, string) {
	saved, _ := monomind.ParseStartupTimeout(s.StartupTimeout) // 0 when unreadable
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
