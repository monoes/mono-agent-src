package workflowbundle

import (
	"errors"
	"fmt"
	"testing"

	"github.com/monoes/mono-agent/internal/automation"
)

// cliError is the fake of the CLI's exit-code errors.
type cliError struct {
	code int
	msg  string
}

func (e *cliError) Error() string { return e.msg }

var errConfirm = errors.New("confirmation required")

// testDeps has no registry: bundling that needs one fails with errNoRegistry.
func testDeps() Deps {
	return Deps{
		OpenRegistry: func() (*automation.Registry, error) { return nil, errNoRegistry },
		InvalidInput: func(format string, a ...interface{}) error {
			return &cliError{code: 3, msg: fmt.Sprintf(format, a...)}
		},
		ErrConfirmationRequired: errConfirm,
	}
}

var errNoRegistry = errors.New("no registry in this test")

// registryDeps is testDeps over an empty registry of its own.
func registryDeps(t *testing.T) Deps {
	t.Helper()
	reg, err := automation.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := testDeps()
	d.OpenRegistry = func() (*automation.Registry, error) { return reg, nil }
	return d
}
