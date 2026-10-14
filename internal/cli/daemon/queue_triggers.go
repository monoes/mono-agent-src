package daemon

import (
	"context"

	dbnodes "github.com/monoes/mono-agent/internal/nodes/db"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// registerQueueTriggers routes the queue/DB consumer triggers (trigger.kafka,
// rabbitmq, amqp, mqtt, redis, postgres) to their consumers; call before
// engine.Start so restored workflows start them. Only the daemon registers
// them: a consumer acknowledges what it receives, so a short-lived engine
// (`workflow activate`) would take messages off the queue and exit before
// running them. The daemon picks up an activation within seconds.
func registerQueueTriggers(engine *workflow.WorkflowEngine, logf func(format string, args ...interface{})) {
	for nodeType := range dbnodes.TriggerSubscribers {
		engine.RegisterTriggerSource(nodeType, &queueTriggerSource{nodeType: nodeType, engine: engine, logf: logf})
	}
}

// queueTriggerSource builds one dbnodes trigger source per activation, so the
// credential resolver is scoped to the activated workflow's profile.
type queueTriggerSource struct {
	nodeType string
	engine   *workflow.WorkflowEngine
	logf     func(format string, args ...interface{})
}

func (s *queueTriggerSource) Activate(w *workflow.Workflow, node *workflow.WorkflowNode, fire func(items []workflow.Item)) (func(), error) {
	profileID := w.ProfileID
	resolve := func(ctx context.Context, cfg map[string]interface{}) (map[string]interface{}, error) {
		return s.engine.ResolveCredentials(vault.ContextWithProfileID(ctx, profileID), s.nodeType, cfg), nil
	}
	wfID, nodeID := w.ID, node.ID
	onErr := func(nodeType string, err error) {
		s.logf("%s (workflow %s, node %s): %v; restarting", nodeType, wfID, nodeID, err)
	}
	src, err := dbnodes.NewTriggerSource(s.nodeType, resolve, onErr)
	if err != nil {
		return nil, err
	}
	return src.Activate(w, node, fire)
}
