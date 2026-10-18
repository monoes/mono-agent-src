package revealgate

import (
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/operator"
)

// A process that is not the operator's own terminal is refused with exit 3,
// the code operator_only and the reason; the operator is not.
func TestRefuse(t *testing.T) {
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
	if err := Refuse("secret reveal"); err != nil {
		t.Fatalf("the operator: %v", err)
	}
	t.Setenv("CLAUDECODE", "1")
	err := Refuse("secret reveal")
	var fields clicore.JSONErrorFields
	if clicore.ExitCodeFor(err) != 3 || !errors.As(err, &fields) || fields.JSONErrorFields()["code"] != "operator_only" {
		t.Fatalf("an agent: %v", err)
	}
	if msg := err.Error(); !strings.HasPrefix(msg, "secret reveal prints decrypted secrets") || !strings.Contains(msg, "CLAUDECODE is set") {
		t.Errorf("the refusal: %s", msg)
	}
}
