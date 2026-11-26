//go:build !windows

package org

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/testhome"
)

// approvalQueue is a stand-in monomind whose pending Bash approvals of role
// dev are the files of a folder. It resolves them as monomind 2.29.0 does:
// the one named by --request, else every pending one of (role, action).
type approvalQueue struct {
	t       *testing.T
	dir     string
	cfg     *testConfig
	project string
}

func newApprovalQueue(t *testing.T, extraCaps string) *approvalQueue {
	t.Helper()
	testhome.SetHome(t, t.TempDir())
	asOrgOperator(t)
	q := &approvalQueue{t: t, dir: t.TempDir(), project: t.TempDir(),
		cfg: &testConfig{DBPath: filepath.Join(t.TempDir(), "o.db"), ProfileID: "default"}}
	script := `#!/bin/sh
Q='` + q.dir + `'
if [ "$1" = "--version" ]; then
  echo '{"v":1,"version":"2.29.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"` + extraCaps + `]}'
  exit 0
fi
verb=$2 role=$4 action=$5
shift 5 2>/dev/null
req=
while [ $# -gt 0 ]; do
  if [ "$1" = "--request" ]; then req=$2; shift; fi
  shift
done
case "$verb" in
approve|deny)
  [ "$role/$action" = "dev/Bash" ] || { echo "no pending approval for $role $action" >&2; exit 1; }
  if [ -n "$req" ]; then
    [ -f "$Q/$req" ] || { echo "no pending approval $req found" >&2; exit 1; }
    mv "$Q/$req" "$Q/.$verb-$req"
  else
    for f in "$Q"/*; do mv "$f" "$Q/.$verb-${f##*/}"; done
  fi
  echo '{"v":1,"ok":true}' ;;
*) echo '{"v":1,"items":[]}' ;;
esac
`
	bin := filepath.Join(t.TempDir(), "monomind")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
	monomind.ResetCapabilityCache()
	t.Cleanup(monomind.ResetCapabilityCache)
	return q
}

func (q *approvalQueue) queue(ids ...string) {
	q.t.Helper()
	for _, id := range ids {
		if err := os.WriteFile(filepath.Join(q.dir, id), nil, 0o600); err != nil {
			q.t.Fatal(err)
		}
	}
}

// files are the ids still pending, then what was resolved (".approve-<id>").
func (q *approvalQueue) files() string {
	q.t.Helper()
	entries, err := os.ReadDir(q.dir)
	if err != nil {
		q.t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return strings.Join(names, " ")
}

func (q *approvalQueue) run(args ...string) error {
	q.t.Helper()
	cmd := newOrgCmd(q.cfg)
	cmd.SetArgs(append([]string{"--project", q.project}, args...))
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var err error
	captureStdout(q.t, func() { err = cmd.Execute() })
	return err
}

const decisionCaps = `,"` + monomind.CapOrgDecisionAttribution + `"`

// --request resolves exactly that request: a call of the same role and action
// queued after the person saw it stays pending.
func TestOrgResolveByRequestIDLeavesALaterCallPending(t *testing.T) {
	for _, verb := range []string{"approve", "deny"} {
		t.Run(verb, func(t *testing.T) {
			q := newApprovalQueue(t, decisionCaps)
			q.queue("apr-seen") // what the card showed
			q.queue("apr-later")
			if err := q.run(verb, "--request=apr-seen", "--", "bud", "dev", "Bash"); err != nil {
				t.Fatal(err)
			}
			if got, want := q.files(), "."+verb+"-apr-seen apr-later"; got != want {
				t.Fatalf("queue = %q, want %q", got, want)
			}
			// A second click, or a request resolved elsewhere, releases nothing.
			if err := q.run(verb, "bud", "dev", "Bash", "--request", "apr-seen"); err == nil {
				t.Error("resolving a request that is no longer pending succeeded")
			}
			if got, want := q.files(), "."+verb+"-apr-seen apr-later"; got != want {
				t.Fatalf("queue after the second try = %q, want %q", got, want)
			}
		})
	}
}

// The role/action form stays for scripts: it resolves every pending call.
func TestOrgResolveByRoleAndActionResolvesEveryPendingCall(t *testing.T) {
	q := newApprovalQueue(t, decisionCaps)
	q.queue("apr-1", "apr-2")
	if err := q.run("approve", "bud", "dev", "Bash"); err != nil {
		t.Fatal(err)
	}
	if got, want := q.files(), ".approve-apr-1 .approve-apr-2"; got != want {
		t.Fatalf("queue = %q, want %q", got, want)
	}
}

// A --request that names nothing usable, or a monomind that cannot take one,
// resolves nothing: neither falls back to the role/action form.
func TestOrgResolveByRequestIDFailsClosed(t *testing.T) {
	for name, tc := range map[string]struct{ caps, request string }{
		"empty id":            {decisionCaps, "--request="},
		"a flag as the id":    {decisionCaps, "--request=--by=rule"},
		"monomind without it": {"", "--request=apr-1"},
	} {
		for _, verb := range []string{"approve", "deny"} {
			t.Run(name+"/"+verb, func(t *testing.T) {
				q := newApprovalQueue(t, tc.caps)
				q.queue("apr-1", "apr-2")
				if err := q.run(verb, "bud", "dev", "Bash", tc.request); err == nil {
					t.Error("succeeded")
				}
				if got := q.files(); got != "apr-1 apr-2" {
					t.Fatalf("queue = %q, want both still pending", got)
				}
			})
		}
	}
}

// The operator gate does not change with an id: approving is the user's own,
// denying stays anyone's.
func TestOrgApproveByRequestIDIsTheOperatorsOnly(t *testing.T) {
	q := newApprovalQueue(t, decisionCaps)
	q.queue("apr-1", "apr-2")
	for _, k := range notTheOperatorMarkers() {
		t.Setenv(k, "1")
		if err := q.run("approve", "bud", "dev", "Bash", "--request", "apr-1"); exitCodeFor(err) != 3 {
			t.Errorf("%s set: approve --request: %v", k, err)
		}
		t.Setenv(k, "")
	}
	asInATurn(t)
	if err := q.run("approve", "bud", "dev", "Bash", "--request", "apr-1"); exitCodeFor(err) != 3 {
		t.Errorf("in a turn: approve --request: %v", err)
	}
	if got := q.files(); got != "apr-1 apr-2" {
		t.Fatalf("queue = %q, want both still pending", got)
	}
	if err := q.run("deny", "bud", "dev", "Bash", "--request", "apr-1"); err != nil {
		t.Fatalf("deny --request: %v", err)
	}
	if got := q.files(); got != ".deny-apr-1 apr-2" {
		t.Fatalf("queue = %q", got)
	}
}
