package main

// CoderSetPeercredBinding is `coder set --peercred-binding on|off`: whether a coder turn's tokens are bound to the turn's
// process (the open-ended mode needs it on). The CLI does every check (the machine can run the binding service, no coder
// turn is running, no open-ended run is open when switching off) and refuses anyone but the user; this window is the
// operator. Returns coder status, whose peercredBinding CoderStatus also reads back.
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
