//go:build linux

package procbind

import (
	"errors"
	"strings"
	"testing"
)

func withSeams(t *testing.T, issuance bool, readyErr error) {
	t.Helper()
	oi, or := issuanceReady, ready
	issuanceReady = func() bool { return issuance }
	ready = func() error { return readyErr }
	t.Cleanup(func() { issuanceReady, ready = oi, or })
}

// While issuance is off, the answer is a refusal whatever else is true; the gate reads the constant, not a setting.
func TestRefusalWhileIssuanceIsOff(t *testing.T) {
	if IssuanceReady {
		t.Skip("issuance is on")
	}
	for _, running := range []bool{false, true} {
		if got := Refusal(running); !strings.Contains(got, "does not issue bound tokens") {
			t.Errorf("running=%v: %q", running, got)
		}
	}
}

func TestRefusalReasons(t *testing.T) {
	cases := map[string]struct {
		issuance bool
		ready    error
		running  bool
		want     string // a fragment; "" means allowed
	}{
		"issuance off":    {false, nil, true, "does not issue bound tokens"},
		"machine cannot":  {true, errors.New("no pidfd"), true, "this machine cannot run it: no pidfd"},
		"service down":    {true, nil, false, "its service is not running"},
		"everything fine": {true, nil, true, ""},
	}
	for name, c := range cases {
		withSeams(t, c.issuance, c.ready)
		got := Refusal(c.running)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

// The real readiness answer names no path, key or token.
func TestReadyReasonsNameNothingSecret(t *testing.T) {
	probes := readyProbes
	t.Cleanup(func() { readyProbes = probes })
	for name, p := range map[string]struct {
		peer  bool
		procs error
	}{
		"no peer pidfd": {false, nil},
		"bad procfs":    {true, errors.New("/home/someone/.monoagent/keys: not this procfs")},
	} {
		readyProbes.peerPidfd = func() bool { return p.peer }
		readyProbes.procs = func() error { return p.procs }
		err := Ready()
		if err == nil {
			t.Errorf("%s: ready", name)
			continue
		}
		for _, bad := range []string{"/home", ".monoagent", "keys", "kek", "vault"} {
			if strings.Contains(err.Error(), bad) {
				t.Errorf("%s: the reason %q mentions %q", name, err, bad)
			}
		}
	}
	readyProbes.peerPidfd = func() bool { return true }
	readyProbes.procs = func() error { return nil }
	if err := Ready(); err != nil {
		t.Errorf("both probes pass but not ready: %v", err)
	}
}
