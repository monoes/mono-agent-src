package config

import (
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
)

func TestRunsScoped(t *testing.T) {
	no := false
	cases := []struct {
		name string
		e    monomind.ScanEntry
		want bool
	}{
		{"older monomind, no modes", monomind.ScanEntry{}, true},
		{"scoped listed", monomind.ScanEntry{AccessModes: []string{"scoped", "full"}}, true},
		{"kilo: full only", monomind.ScanEntry{AccessModes: []string{"full"}}, false},
		{"not executable", monomind.ScanEntry{ExecutionSupported: &no}, false},
	}
	for _, c := range cases {
		if got := runsScoped(&c.e); got != c.want {
			t.Errorf("%s: runsScoped = %v, want %v", c.name, got, c.want)
		}
	}
}
