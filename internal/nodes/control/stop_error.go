package control

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/workflow"
)

// StopErrorNode terminates the workflow with a user-defined error message.
// Config fields:
//
//	"message" (string, required): error message to surface
type StopErrorNode struct{}

func (n *StopErrorNode) Type() string { return "core.stop_error" }

// PublishesToThirdParty: core.stop_error sends nothing off this machine (issue #1418).
func (StopErrorNode) PublishesToThirdParty() bool { return false }

func (n *StopErrorNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	message, _ := config["message"].(string)
	if message == "" {
		return nil, fmt.Errorf("%w: stop_error node requires a non-empty \"message\"", workflow.ErrInvalidConfig)
	}

	return nil, fmt.Errorf("StopError: %s", message)
}
