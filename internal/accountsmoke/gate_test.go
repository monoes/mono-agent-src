//go:build devaccount && !windows

package accountsmoke

import (
	"os"
	"strings"
	"testing"
	"time"
)

// shortToken is how long the fake's access tokens live in the scenario that waits for a daemon's
// refresher to notice a change: a daemon refreshes at half of it, so it learns of a block within
// seconds instead of half an hour.
const shortToken = 15 * time.Second

// Acceptance 3: signed in, monoes.me unreachable: the work goes on until 24 hours after the newest
// token was issued, then the machine is locked; work in flight at that moment finishes the node it is
// in (this job has one node, so it ends SUCCESS; a run with a next node ends there, ruling R4).
func TestUnreachableIsGraceUntilTwentyFourHours(t *testing.T) {
	r := newRig(t, rigOptions{enforce: past})
	r.signIn()
	r.edge.set(modeDown)
	r.backdate(2 * time.Hour) // the access token expired an hour ago, and monoes.me cannot be asked for another

	if st, code := r.status(); st.State != "grace" || st.Reason != "unreachable" || code != 0 || r.edge.dropped() == 0 {
		t.Fatalf("expired token, monoes.me down: exit %d, %+v, %d requests reached monoes.me", code, st, r.edge.dropped())
	}
	res := r.run("--json", "workflow", "list")
	mustExit(t, res, 0)
	if !strings.Contains(res.stderr, graceLine) || strings.Contains(res.stdout, graceLine) || !strings.HasPrefix(strings.TrimSpace(res.stdout), "[") {
		t.Fatalf("grace is one line on stderr and never on stdout:\nstdout: %s\nstderr: %s", res.stdout, res.stderr)
	}

	// A daemon in grace runs a 40-second job; the 24 hours run out while it runs.
	r.startDaemon()
	id := r.enqueue(r.workflow(waitWorkflow("smoke-forty", 40), true))
	r.waitFor("the job to be running in the daemon", 60*time.Second, func() bool { return r.execution(id).Status == "RUNNING" })
	r.backdate(24*time.Hour + time.Minute)
	r.waitFor("the daemon to lock", 60*time.Second, func() bool { return r.accountState() == "locked" })
	if got := r.execution(id).Status; got != "RUNNING" {
		t.Fatalf("the job was %s when the 24 hours ran out: unreachable is not a refusal, so the node in flight finishes", got)
	}
	r.assertLocked(r.run("--json", "workflow", "list"), "expired", true)
	r.waitFor("the job to finish", 90*time.Second, func() bool { return r.execution(id).Status != "RUNNING" })
	if got := r.execution(id).Status; got != "SUCCESS" {
		t.Fatalf("the job ended %s, want SUCCESS", got)
	}

	r.edge.set(modePass) // monoes.me is back: one sign-in and the machine works again
	r.signIn()
	mustExit(t, r.run("workflow", "list"), 0)
}

// D27: only invalid_grant answered to a refresh-token grant is a refusal. A server error, a client
// or a resource monoes.me does not know, a page that is not JSON: each is trouble on the way, so
// the machine stays in grace with reason server_error, the refresh token is kept (it may well be
// good) and nothing is refused. (A24: the server error and the page that is not JSON are the two
// of these whose outcome is unknown, a 5xx that may follow a rotation and a 200 that holds no token
// set, so the guard keeps pending_since beside the token and retries at once for 240 seconds; this
// test stays well inside that. The unknown client and resource are complete 4xx answers: settled.)
func TestAnswersThatAreNotARefusalKeepTheGrace(t *testing.T) {
	for _, c := range []struct {
		name string
		mode int32
	}{
		{"a server error", modeServerError},
		{"an unknown client", modeInvalidClient},
		{"an unknown resource", modeInvalidTarget},
		{"a page that is not JSON", modeGarbage},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, rigOptions{enforce: past})
			r.signIn()
			r.edge.set(c.mode)
			r.backdate(2 * time.Hour) // the access token expired an hour ago: the next command asks for another

			if st, code := r.status(); st.State != "grace" || st.Reason != "server_error" || code != 0 || r.edge.refreshes() == 0 {
				t.Fatalf("expired token, refresh answered by %s: exit %d, %+v, %d refresh attempts; only invalid_grant locks", c.name, code, st, r.edge.refreshes())
			}
			res := r.run("--json", "workflow", "list")
			mustExit(t, res, 0)
			if !strings.Contains(res.stderr, graceLine) || strings.Contains(res.stdout, graceLine) {
				t.Fatalf("grace is one line on stderr and never on stdout:\nstdout: %s\nstderr: %s", res.stdout, res.stderr)
			}
			if _, err := os.Stat(r.refreshFile()); err != nil {
				t.Fatalf("the refresh token must be kept: %v", err)
			}
			// monoes.me recovers: the refresh token that was kept is still good, so the next refresh
			// ends the grace (aged again, so that the failed attempt's minute is over).
			r.edge.set(modePass)
			r.backdate(2 * time.Hour)
			if st, code := r.status(); st.State != "ok" || code != 0 {
				t.Fatalf("after monoes.me recovered: exit %d, %+v", code, st)
			}
		})
	}
}

// Spec 4.7 and A9: a refreshed token signed by a key this build does not pin is not stored, and the
// grace shows server_error; when the grace ends the verdict is locked(key_unknown), the case that
// wants an update and not another sign-in, and doctor's fix is the update.
func TestAnUnknownSigningKeyIsTheRunUpdateCase(t *testing.T) {
	r := newRig(t, rigOptions{enforce: past})
	r.signIn()
	r.edge.set(modeUnknownKey)
	r.backdate(2 * time.Hour)
	if st, code := r.status(); st.State != "grace" || st.Reason != "server_error" || code != 0 || r.edge.refreshes() == 0 {
		t.Fatalf("a refresh answered with a token of an unknown key: exit %d, %+v", code, st)
	}
	r.backdate(25 * time.Hour) // the grace is over
	if st, code := r.status(); st.State != "locked" || st.Reason != "key_unknown" || code != 4 {
		t.Fatalf("at the end of the grace: exit %d, %+v, want locked(key_unknown)", code, st)
	}
	r.assertLocked(r.run("--json", "workflow", "list"), "key_unknown", true)
	var report struct {
		Results []struct {
			ID, Status string
			Fix        *struct{ ID string }
		}
	}
	mustJSON(t, r.run("--json", "doctor", "--check", "core.monoes_account").stdout, &report)
	found := false
	for _, x := range report.Results {
		if x.ID == "core.monoes_account" {
			found = true
			if x.Status != "fail" || x.Fix == nil || x.Fix.ID != "core.update.install" {
				t.Fatalf("doctor row for an unknown key: %+v, want fail with the update as its fix", x)
			}
		}
	}
	if !found {
		t.Fatal("doctor has no core.monoes_account row")
	}
}

// Acceptance 2: signed in, then blocked on monoes.me: the machine is locked within one refresh
// interval, work in flight in the daemon is cancelled, the daemon stays up and resumes by itself
// when a valid session appears.
func TestBlockedAccountLocksAndCancelsWorkInFlight(t *testing.T) {
	r := newRig(t, rigOptions{enforce: past, ttl: shortToken})
	r.signIn()
	d := r.startDaemon()
	r.waitFor("the daemon to report a signed-in account", 30*time.Second, func() bool { return r.accountState() == "ok" })
	pid, _, _ := r.heartbeat()
	// The slow run must stay RUNNING (work in flight) until the refusal
	// cancels it: core.wait holds waits up to 60s in memory and pauses longer
	// ones durably as WAITING, which frees the run's slot and is not in flight.
	// 55s outlasts the 15s token's refusal by far.
	fast, slow := r.workflow(waitWorkflow("smoke-fast", 1), true), r.workflow(waitWorkflow("smoke-slow", 55), true)
	id := r.enqueue(slow)
	r.waitFor("the slow run to be running in the daemon", 60*time.Second, func() bool {
		e := r.execution(id)
		return e.Status == "RUNNING" && e.PID == pid
	})

	r.edge.set(modeInvalidGrant) // monoes.me answers no
	r.waitFor("the run in flight to be cancelled", 120*time.Second, func() bool { return r.execution(id).Status == "CANCELLED" })
	r.waitFor("the heartbeat to say locked", 30*time.Second, func() bool { return r.accountState() == "locked" })
	if _, _, reason := r.heartbeat(); reason != "refused" {
		t.Fatalf("the heartbeat's reason is %q, want refused", reason)
	}
	if !d.alive() {
		t.Fatal("a locked daemon stays up: a service manager would only restart it in a loop")
	}
	r.assertLocked(r.run("--json", "workflow", "list"), "refused", true)
	if _, err := os.Stat(r.refreshFile()); !os.IsNotExist(err) {
		t.Fatalf("a refusal deletes the refresh token: %v", err)
	}

	r.edge.set(modePass)
	r.signIn()
	r.waitFor("the daemon to resume by itself", 60*time.Second, func() bool { return r.accountState() == "ok" })
	again := r.enqueue(fast)
	r.waitFor("the resumed daemon to run work", 60*time.Second, func() bool { return r.execution(again).Status == "SUCCESS" })
}
