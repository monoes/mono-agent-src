package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/library/libraryfake"
)

// accountStatus runs `account status` and returns the document it printed with the exit code.
func (f *libFixture) accountStatus(args ...string) (account.Status, int) {
	f.t.Helper()
	var st account.Status
	out, err := f.run(append([]string{"account", "status"}, args...)...)
	if e := json.Unmarshal([]byte(lastJSONObject([]byte(out))), &st); e != nil {
		f.t.Fatalf("account status: not JSON: %v\n%s", e, out)
	}
	return st, exitCodeFor(err)
}

// backdate makes the stored session's last refresh attempt d older, as if the
// previous command had run d ago. A guard holds a second attempt off for a minute
// (spec §4.4), so a test that wants the next command to renew moves the file's
// clock, not its own.
func (f *libFixture) backdate(d time.Duration) {
	f.t.Helper()
	store := account.OpenStore(filepath.Join(f.home, ".monoagent", "account"), account.NewMemorySealer())
	sess, err := store.Load()
	if err != nil || sess == nil {
		f.t.Fatalf("no session to backdate: %v", err)
	}
	sess.LastAttempt = sess.LastAttempt.Add(-d)
	if err := store.Save(sess); err != nil {
		f.t.Fatal(err)
	}
}

func TestAccountLoginStatusLogout(t *testing.T) {
	f := newLibFixture(t)
	st, code := f.accountStatus()
	if code != 4 || st.V != 1 || st.State != account.StateLocked || st.Reason != account.ReasonNotLoggedIn {
		t.Fatalf("logged out: exit %d, %+v", code, st)
	}
	if _, err := os.Stat(filepath.Join(f.home, ".monoagent", "account")); !os.IsNotExist(err) {
		t.Fatalf("a status call on a logged-out machine made the account folder: %v", err)
	}

	// The sign-in URL goes to stderr as one JSON line, for the desktop, with the audience in it.
	_, errOut, err := runCLI(t, f.home, "--json", "account", "login")
	if err != nil || !strings.Contains(errOut, `"kind":"url"`) || !strings.Contains(errOut, "resource=") {
		t.Fatalf("login: %v\nstderr: %s", err, errOut)
	}
	st, code = f.accountStatus()
	if code != 0 || st.State != account.StateOK || st.User == nil || st.User.Username != "ada" || st.Plan != "free" {
		t.Fatalf("after login: exit %d, %+v", code, st)
	}
	if off, code := f.accountStatus("--offline"); code != 0 || off.State != account.StateOK {
		t.Fatalf("offline: exit %d, %+v", code, off)
	}
	var out map[string]any
	f.must(&out, "account", "logout")
	if out["logged_out"] != true {
		t.Fatalf("logout = %v", out)
	}
	if st, code = f.accountStatus(); code != 4 || st.Reason != account.ReasonNotLoggedIn {
		t.Fatalf("after logout: exit %d, %+v", code, st)
	}
	// What a person reads after logging out is the plain "not logged in": the record that logout
	// keeps (a session with no token, A23) is not a login and says nothing of its own. Logging out
	// again is not an error.
	if _, _, err := runCLI(t, f.home, "account", "status"); exitCodeFor(err) != 4 || !strings.Contains(err.Error(), "Not logged in to monoes.me. Run: monoagentcli account login") {
		t.Fatalf("account status after logout: %v", err)
	}
	f.must(nil, "account", "logout")
}

// Spec D5 and D27: a blocked account is locked at its next renewal and signs in again once unblocked, while a
// monoes.me that merely fails leaves it ok (a 500 is an unknown outcome, A24: the next renewal retries at once).
func TestAccountStatusFollowsMonoesMe(t *testing.T) {
	f := newLibFixture(t)
	f.fake.AccessTTL = 2 * time.Minute // inside the renewal margin: a renewal is due
	f.must(nil, "account", "login")
	f.backdate(2 * time.Minute)
	if st, code := f.accountStatus(); code != 0 || st.State != account.StateOK || f.fake.Refreshes != 1 {
		t.Fatalf("a due session: exit %d, %+v (renewals %d)", code, st, f.fake.Refreshes)
	}

	f.fake.SetRefreshMode(libraryfake.RefreshServerError)
	f.backdate(2 * time.Minute)
	if st, code := f.accountStatus(); code != 0 || st.State != account.StateOK {
		t.Fatalf("monoes.me failing: exit %d, %+v", code, st)
	}
	f.fake.SetRefreshMode(libraryfake.RefreshOK)

	f.fake.Block("u-ada")
	f.backdate(2 * time.Minute)
	if st, code := f.accountStatus(); code != 4 || st.State != account.StateLocked || st.Reason != account.ReasonRefused {
		t.Fatalf("blocked: exit %d, %+v", code, st)
	}
	if _, err := f.run("account", "login"); exitCodeFor(err) != 4 {
		t.Fatalf("a blocked account signed in: %v", err)
	}
	f.fake.Unblock("u-ada")
	var st account.Status
	f.must(&st, "account", "login")
	if st.State != account.StateOK {
		t.Fatalf("after the unblock: %+v", st)
	}
}
