package image

import (
	"context"

	"github.com/monoes/mono-agent/internal/workflow"
)

// removedNode stands in for an image node whose feature was removed. It stays
// registered so a saved workflow that still uses it fails with the message from
// workflow.DeprecatedNodeTypes instead of "unknown node type". It reads no
// config, opens no file and sends nothing.
type removedNode struct{ typeName string }

func (n *removedNode) Type() string { return n.typeName }

func (n *removedNode) Execute(context.Context, workflow.NodeInput, map[string]interface{}) ([]workflow.NodeOutput, error) {
	return nil, workflow.DeprecatedNodeError(n.typeName)
}

// PublishesToThirdParty: nothing runs, so nothing leaves this machine (issue #1418).
func (*removedNode) PublishesToThirdParty() bool { return false }
