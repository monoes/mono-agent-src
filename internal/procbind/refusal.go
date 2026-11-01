package procbind

// The seams Refusal reads; tests replace them.
var (
	issuanceReady = func() bool { return IssuanceReady }
	ready         = Ready
)

// Refusal is why peer-credential binding may not run, as a short cause, or "" when it may. It is asked when the
// operator switches the setting on and again at every turn start (a hand-edited setting must still refuse), with
// whether the binding service is running. A cause is a fixed text for the operator: it names no path, key or
// token. The setting never degrades to a weaker mode: if it is on and this is not "", the turn is refused.
func Refusal(serviceRunning bool) string {
	err := ready()
	switch {
	case !issuanceReady():
		return "this build does not issue bound tokens yet"
	case err != nil:
		return "this machine cannot run it: " + err.Error()
	case !serviceRunning:
		return "its service is not running"
	}
	return ""
}

// IssuanceOn says whether bound tokens may be issued and accepted. It is the constant IssuanceReady; the stores install
// the real verifier by it and the gate reads it, so one value decides everything.
func IssuanceOn() bool { return issuanceReady() }

// ForTestSetIssuance overrides IssuanceOn for the life of a test. It takes the test (anything with Cleanup), so it
// cannot be used without one; a test (procbind's own) fails if any non-test file calls it.
func ForTestSetIssuance(t interface {
	Helper()
	Cleanup(func())
}, on bool) {
	t.Helper()
	old := issuanceReady
	issuanceReady = func() bool { return on }
	t.Cleanup(func() { issuanceReady = old })
}
