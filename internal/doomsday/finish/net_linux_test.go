//go:build linux

package finish

import (
	"os"
	"strings"
	"testing"
)

// Where an unprivileged user namespace exists, the criterion runs in a network namespace of its own: a different
// namespace than ours, recorded as enforced. Where it does not, the record says declared-only.
func TestCriterionRunsInItsOwnNetworkNamespaceWhereThePlatformAllows(t *testing.T) {
	r := newRig(t)
	ours, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		t.Skip("no /proc/self/ns/net")
	}
	o := r.run("command", "readlink /proc/self/ns/net", "")
	if !netDenyAvailable() {
		if o.Net != NetDeclared {
			t.Fatalf("no user namespace here, yet net = %q", o.Net)
		}
		t.Skip("unprivileged user namespaces are not available here; only the declared-only record was checked")
	}
	if o.Net != NetEnforced || strings.TrimSpace(o.Stdout) == ours || !strings.HasPrefix(o.Stdout, "net:[") {
		t.Fatalf("the program shared our network namespace (%q, ours %q): %+v", strings.TrimSpace(o.Stdout), ours, o)
	}
}

// Mutant: dropping denyNet(cmd) makes the program share our namespace, and the test above fails; the injected probe
// answer "unavailable" makes the sandbox start the program without it and say declared-only.
func TestWithoutTheDenialTheResultSaysDeclaredOnly(t *testing.T) {
	r := newRig(t)
	r.sb.NetDenyAvailable = func() bool { return false }
	if o := r.run("command", "echo hi", "hi"); o.Result != ResPass || o.Net != NetDeclared {
		t.Fatalf("%+v", o)
	}
}
