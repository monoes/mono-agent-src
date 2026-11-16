package orgsvc

import "testing"

// The daemon never re-signs an org it reconciles: it cannot tell a coder turn's row write from the
// operator's. A reconciler built without Unsigned would sign it.
func TestDaemonReconcilerLeavesSigningToTheOperator(t *testing.T) {
	if !(&Services{}).reconciler().Unsigned {
		t.Fatal("the daemon's reconciler must save unsigned")
	}
}
