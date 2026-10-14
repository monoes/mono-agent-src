package doctor

import (
	"context"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/health"
)

// addMonoesAccountHook feeds the doctor's core.monoes_account row from the
// process guard's verdict. It is a read of the session already loaded: no
// refresh and no network, because a check must change nothing.
func addMonoesAccountHook(env *health.Env) {
	env.MonoesAccount = func(context.Context) health.AccountInfo {
		return healthAccount(account.CurrentStatus())
	}
}

func healthAccount(st account.Status) health.AccountInfo {
	info := health.AccountInfo{State: string(st.State), Reason: string(st.Reason), GraceUntil: st.GraceUntil,
		EnforceFrom: st.EnforceFrom, Enforced: st.Enforced}
	if st.User != nil {
		info.Email = st.User.Email
	}
	return info
}
