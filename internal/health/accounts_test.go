package health

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConnectionsCheck(t *testing.T) {
	env := &Env{
		Connections: func(context.Context) ([]ConnectionInfo, error) {
			return []ConnectionInfo{
				{ID: "aaaaaaaa-1", Platform: "github", Label: "GitHub – me", Method: "oauth", HasRefreshToken: true},
				{ID: "bbbbbbbb-2", Platform: "notion", Method: "oauth"},
				{ID: "cccccccc-3", Platform: "slack", Method: "api_key"},
				{ID: "dddddddd-4", Platform: "gmail", Method: "oauth", HasRefreshToken: true},
				{ID: "eeeeeeee-5", Platform: "linear", Method: "oauth", HasRefreshToken: true},
			}, nil
		},
		TestConnection: func(_ context.Context, id string) error {
			switch id {
			case "aaaaaaaa-1", "bbbbbbbb-2":
				return CredentialsRejected(errors.New("unexpected status 401"))
			case "dddddddd-4":
				return errors.New("unexpected status 400")
			case "eeeeeeee-5":
				return Unreachable(errors.New("unexpected status 503"))
			}
			return nil
		},
	}
	reg := NewRegistry([]Check{{ID: CheckConnections, Group: GroupAccounts, Title: "Connections", Run: checkConnections}}, accountFixes())
	rep := reg.Run(context.Background(), env, Options{})
	got := byID(rep)
	if p := got[CheckConnections]; p.Status != StatusWarn || p.Summary != "3 of 5 failing, 1 not answering" {
		t.Fatalf("parent: %+v", p)
	}
	// Rejected, with a refresh token → silent refresh (auto).
	if f := got["accounts.connection.aaaaaaaa"].Fix; f == nil || f.ID != FixConnectionRefresh+":aaaaaaaa-1" || f.Safety != SafetyAuto ||
		f.Command != "monoagentcli connect refresh aaaaaaaa-1" {
		t.Errorf("rejected, refreshable: %+v", f)
	}
	// No refresh token → reconnect, naming the row's platform.
	if f := got["accounts.connection.bbbbbbbb"].Fix; f == nil || f.ID != FixReconnect || f.Safety != SafetyManual ||
		!strings.HasPrefix(f.Command, "monoagentcli connect notion") {
		t.Errorf("no refresh token: %+v", f)
	}
	if got["accounts.connection.cccccccc"].Status != StatusOK {
		t.Errorf("working: %+v", got["accounts.connection.cccccccc"])
	}
	// A failure of unknown cause → the refresh asks first.
	if f := got["accounts.connection.dddddddd"].Fix; f == nil || f.ID != FixConnectionTryRefresh+":dddddddd-4" || f.Safety != SafetyConfirm {
		t.Errorf("unknown failure: %+v", f)
	}
	// Not answering → a warning with nothing to fix: no refresh, which on
	// many providers rotates the refresh token.
	if r := got["accounts.connection.eeeeeeee"]; r.Status != StatusWarn || r.Fix != nil {
		t.Errorf("unreachable: %+v", r)
	}

	env.Connections = func(context.Context) ([]ConnectionInfo, error) { return nil, nil }
	if res := checkConnections(context.Background(), env); res.Status != StatusInfo {
		t.Errorf("none: %+v", res)
	}
}

func TestClassify(t *testing.T) {
	for err, want := range map[error]failureKind{
		errors.New("x"):                                   failUnknown,
		CredentialsRejected(errors.New("x")):              failRejected,
		Unreachable(errors.New("x")):                      failUnreachable,
		context.DeadlineExceeded:                          failUnreachable,
		errors.Join(errors.New("a"), Unreachable(nil)):    failUnreachable,
		CredentialsRejected(errors.New("status 401 foo")): failRejected,
	} {
		if got := classify(err); got != want {
			t.Errorf("classify(%v) = %v, want %v", err, got, want)
		}
	}
	if CredentialsRejected(errors.New("msg")).Error() != "msg" {
		t.Error("marking an error changed its message")
	}
}

// Connections are tested a few at a time, and one that never answers (even
// ignoring its context) costs its own row only.
func TestConnectionsTestedInParallelWithinLimits(t *testing.T) {
	defer func(p int, it, m time.Duration) { accountParallel, accountItemTimeout, accountMargin = p, it, m }(
		accountParallel, accountItemTimeout, accountMargin)
	accountParallel, accountItemTimeout, accountMargin = 2, 150*time.Millisecond, 20*time.Millisecond

	var running, peak int32
	// The slow test outlives checkConnections, which abandons it. Release
	// it and wait for it to return before the assertions, so nothing still
	// writes running or peak while they are read.
	hang, slowDone := make(chan struct{}), make(chan struct{})
	var releaseSlow, slowReturned sync.Once
	release := func() {
		releaseSlow.Do(func() { close(hang) })
		select {
		case <-slowDone:
		case <-time.After(5 * time.Second):
			t.Fatal("the slow connection test never returned (was it started?)")
		}
	}
	defer releaseSlow.Do(func() { close(hang) })
	env := &Env{
		Connections: func(context.Context) ([]ConnectionInfo, error) {
			return []ConnectionInfo{{ID: "slow"}, {ID: "a"}, {ID: "b"}, {ID: "c"}}, nil
		},
		TestConnection: func(_ context.Context, id string) error {
			if id == "slow" {
				defer slowReturned.Do(func() { close(slowDone) }) // runs last
			}
			n := atomic.AddInt32(&running, 1)
			defer atomic.AddInt32(&running, -1)
			for {
				p := atomic.LoadInt32(&peak)
				if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
					break
				}
			}
			if id == "slow" {
				<-hang // ignores its context
			}
			time.Sleep(20 * time.Millisecond)
			return nil
		},
	}
	start := time.Now()
	res := checkConnections(context.Background(), env)
	took := time.Since(start)
	release()
	if took > time.Second {
		t.Fatalf("took %s", took)
	}
	if peak := atomic.LoadInt32(&peak); peak > 2 {
		t.Errorf("%d tests ran at once, limit 2", peak)
	}
	if len(res.Children) != 4 || res.Children[0].Status != StatusWarn || !strings.Contains(res.Children[0].Detail, "no answer within") {
		t.Fatalf("slow row: %+v", res.Children)
	}
	for _, c := range res.Children[1:] {
		if c.Status != StatusOK {
			t.Errorf("row %s: %+v", c.ID, c)
		}
	}

	// A check deadline too close to test anything: every row is still
	// reported, as not tested.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	res = checkConnections(ctx, env)
	if len(res.Children) != 4 || res.Summary != "4 not tested in time" || res.Children[1].Status != StatusSkip {
		t.Errorf("out of time: %s %+v", res.Summary, res.Children)
	}
}

func TestAccountChecksAreDeepOnlyExceptLogins(t *testing.T) {
	for _, c := range accountChecks() {
		if want := c.ID != CheckLogins; c.Network != want {
			t.Errorf("%s: Network = %v, want %v", c.ID, c.Network, want)
		}
	}
}

func TestLogins(t *testing.T) {
	ctx := context.Background()
	env := &Env{
		LoginSessions: func(context.Context) ([]SessionInfo, error) {
			return []SessionInfo{
				{Platform: "x", Username: "me", Expiry: time.Now().Add(-time.Hour)},
				{Platform: "instagram", Username: "me.too@example", Expiry: time.Now().Add(time.Hour)},
			}, nil
		},
	}
	res := checkLogins(ctx, env)
	if res.Status != StatusWarn || res.Children[0].FixID != FixLogin || res.Children[1].Status != StatusOK {
		t.Fatalf("logins: %+v", res)
	}
	if res.Children[0].FixCommand != "monoagentcli login x" {
		t.Errorf("login fix command = %q", res.Children[0].FixCommand)
	}
	// Row ids name the session, not its position.
	if res.Children[0].ID != "accounts.login.x.me" || res.Children[1].ID != "accounts.login.instagram.me_too_example" {
		t.Errorf("login ids: %q, %q", res.Children[0].ID, res.Children[1].ID)
	}

	env.LoginSessions = func(context.Context) ([]SessionInfo, error) { return nil, nil }
	if res := checkLogins(ctx, env); res.Status != StatusInfo {
		t.Errorf("no logins: %+v", res)
	}
}

// Login row ids stay the same whatever order the sessions come in, and
// usernames that sanitize alike still get distinct ids.
func TestLoginRowIDsStable(t *testing.T) {
	a := []SessionInfo{{Platform: "x", Username: "a.b"}, {Platform: "x", Username: "a_b"}, {Platform: "linkedin", Username: "me"}}
	b := []SessionInfo{a[2], a[1], a[0]}
	ia, ib := loginRowIDs(a), loginRowIDs(b)
	if ia[0] != ib[2] || ia[1] != ib[1] || ia[2] != ib[0] {
		t.Fatalf("ids depend on order: %v vs %v", ia, ib)
	}
	if ia[0] == ia[1] || !strings.HasPrefix(ia[0], "accounts.login.x.a_b-") {
		t.Errorf("colliding names: %v", ia)
	}
	if ia[2] != "accounts.login.linkedin.me" {
		t.Errorf("plain id: %q", ia[2])
	}
}

func TestLoginsDoNotClaimValidityOrLeakUsernames(t *testing.T) {
	ctx := context.Background()
	env := &Env{LoginSessions: func(context.Context) ([]SessionInfo, error) {
		return []SessionInfo{{Platform: "instagram", Username: "secret.person", Expiry: time.Now().Add(time.Hour)}}, nil
	}}
	res := checkLogins(ctx, env)
	row := res.Children[0]
	if strings.Contains(row.Summary, "secret.person") || strings.Contains(row.Summary, "valid") || row.Detail != "secret.person" {
		t.Errorf("summary %q detail %q", row.Summary, row.Detail)
	}
	if strings.Contains(res.Summary, "valid") {
		t.Errorf("summary %q claims validity", res.Summary)
	}
	env.LoginSessions = func(context.Context) ([]SessionInfo, error) { return nil, errors.New("db locked") }
	if res := checkLogins(ctx, env); res.Status != StatusWarn {
		t.Errorf("a read error is %q, want warn", res.Status)
	}
}
