package main

// CoderSetPeercredBinding is `coder set --peercred-binding on|off`: whether a turn's tokens are bound to the turn's
// process by the kernel-checked binding service. The open-ended mode needs it on. The CLI holds every check (the
// machine must be able to run the service, no turn may be running, it cannot go off under an open run) and refuses
// anyone but the user, so this only passes the choice on and returns the CLI's answer verbatim: the coder status,
// whose peercredBinding CoderStatus also reads back, or {error}.
func (a *App) CoderSetPeercredBinding(on bool) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	v := "off"
	if on {
		v = "on"
	}
	return a.jsonResult("coder", "set", "--peercred-binding", v)
}
