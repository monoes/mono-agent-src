package monomind

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// An override that is no duration is ignored and said so, once; one above the maximum is cut to it.
func TestStartupTimeoutEnvIsReadStrictly(t *testing.T) {
	var warned bytes.Buffer
	saved := startupWarnOut
	startupWarnOut = &warned
	t.Cleanup(func() { startupWarnOut = saved })
	for _, tc := range []struct {
		env    string
		want   time.Duration
		source string
		warns  bool
	}{
		{"", 5 * time.Minute, StartupTimeoutFromSetting, false},
		{"0", 5 * time.Minute, StartupTimeoutFromSetting, false},
		{" 4m ", 4 * time.Minute, StartupTimeoutFromEnv, false},
		{"7mins", 5 * time.Minute, StartupTimeoutFromSetting, true},
		{"7 min", 5 * time.Minute, StartupTimeoutFromSetting, true},
		{"180", 5 * time.Minute, StartupTimeoutFromSetting, true},
		{"-4m", 5 * time.Minute, StartupTimeoutFromSetting, true},
		{"700h", MaxStartupTimeout, StartupTimeoutFromEnv, true},
	} {
		t.Setenv(StartupTimeoutEnv, tc.env)
		warned.Reset()
		got, source := StartupTimeoutSetting(5 * time.Minute)
		if got != tc.want || source != tc.source {
			t.Errorf("%q: %v from %s, want %v from %s", tc.env, got, source, tc.want, tc.source)
		}
		said := warned.String()
		if tc.warns != strings.Contains(said, StartupTimeoutEnv) || tc.warns != strings.Contains(said, tc.env) && tc.env != "" && tc.env != "0" {
			t.Errorf("%q: warning %q, want one: %v", tc.env, said, tc.warns)
		}
		warned.Reset()
		StartupTimeoutSetting(0)
		if warned.Len() != 0 {
			t.Errorf("%q: warned twice: %q", tc.env, warned.String())
		}
	}
	t.Setenv(StartupTimeoutEnv, "")
	if got, _ := StartupTimeoutSetting(700 * time.Hour); got != MaxStartupTimeout {
		t.Errorf("a saved %v is not cut to the maximum: %v", 700*time.Hour, got)
	}
	if got := startupTimeoutFor(ExecOptions{StartupTimeout: 700 * time.Hour}); got != MaxStartupTimeout {
		t.Errorf("an option of 700h is not cut to the maximum: %v", got)
	}
}
