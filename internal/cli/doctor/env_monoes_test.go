package doctor

import (
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/health"
)

func TestHealthAccountMapsTheStatus(t *testing.T) {
	date, until := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 6, 14, 2, 0, 0, time.UTC)
	got := healthAccount(account.Status{State: account.StateGrace, Reason: account.ReasonServerError, User: &account.User{Email: "a@b.c"},
		GraceUntil: until, EnforceFrom: date, Enforced: true})
	want := health.AccountInfo{State: "grace", Reason: "server_error", Email: "a@b.c", GraceUntil: until, EnforceFrom: date, Enforced: true}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := healthAccount(account.Status{State: account.StateLocked, Reason: account.ReasonNotLoggedIn}); got.Email != "" || got.State != "locked" {
		t.Errorf("no user: %+v", got)
	}
}
