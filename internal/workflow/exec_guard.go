package workflow

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/monoes/mono-agent/internal/secrets"
)

// ExecGuard sees every node before it runs. When it reports handled, its
// outputs and error stand in for the node's own and the node does not run.
// Outbound sends use it to wait for a person's approval (internal/hilsend).
type ExecGuard func(ctx context.Context, nodeType string, in NodeInput, config map[string]interface{}) (out []NodeOutput, handled bool, err error)

var execGuard atomic.Pointer[ExecGuard]

// SetExecGuard installs g for the process; nil removes it.
func SetExecGuard(g ExecGuard) {
	if g == nil {
		execGuard.Store(nil)
		return
	}
	execGuard.Store(&g)
}

// RunExecGuard runs the installed guard, if any.
func RunExecGuard(ctx context.Context, nodeType string, in NodeInput, config map[string]interface{}) ([]NodeOutput, bool, error) {
	if g := execGuard.Load(); g != nil {
		return (*g)(ctx, nodeType, in, config)
	}
	return nil, false, nil
}

// Declarer is what an executor says about whether it publishes to third
// parties: false when it sends nothing off this machine. An executor that
// does not declare it publishes (fail closed; the pin is in noderegistry).
type Declarer interface{ PublishesToThirdParty() bool }

// StoreWriter is what a local-only executor says when it puts what it is
// given where another run can read it back (a table, a file, a record, the
// vault): a run under agent access that has read a secret may not write it
// there (secrets.CheckStoreWrite).
type StoreWriter interface{ WritesLocalStore() bool }

// Publishes reports whether e publishes to third parties.
func Publishes(e NodeExecutor) bool {
	if d, ok := e.(Declarer); ok {
		return d.PublishesToThirdParty()
	}
	return true
}

// RunExecGuardFor is the seam every dispatch point passes through. Under
// agent access a node that publishes is refused, before the guard and so
// before any approval card, when its run has read a granted secret
// (secrets.CheckPublish); --any-host does not lift it. Everything else is
// RunExecGuard's. A run that is not under agent access passes unchanged.
func RunExecGuardFor(ctx context.Context, e NodeExecutor, in NodeInput, config map[string]interface{}) ([]NodeOutput, bool, error) {
	if err := secrets.CheckPublish(ctx, e.Type(), Publishes(e)); err != nil {
		return nil, true, err
	}
	if w, ok := e.(StoreWriter); ok && w.WritesLocalStore() && secrets.RunHasRead(ctx) {
		if err := checkStoreWrite(ctx, e.Type(), in, config); err != nil {
			return nil, true, err
		}
	}
	return RunExecGuard(ctx, e.Type(), in, config)
}

// checkStoreWrite scans what a store writer is given (its input items, binary
// data included, its trigger data and its configuration) for a value the run
// read; when a field holds an expression ({{ ... }}) it also scans the outputs
// of the earlier nodes it could reach, one node at a time so a refusal names
// the node.
func checkStoreWrite(ctx context.Context, nodeType string, in NodeInput, config map[string]interface{}) error {
	items := make([]map[string]interface{}, 0, len(in.Items))
	var binary strings.Builder
	for _, it := range in.Items {
		items = append(items, it.JSON)
		binaryText(&binary, it)
	}
	rawItems, _ := json.Marshal(items)
	rawConfig, _ := json.Marshal(config)
	fields := []secrets.StoreField{{Name: "input items", Text: string(rawItems)}, {Name: "configuration", Text: string(rawConfig)}}
	if binary.Len() > 0 {
		fields = append(fields, secrets.StoreField{Name: "binary data", Text: binary.String()})
	}
	if len(in.Trigger) > 0 {
		rawTrigger, _ := json.Marshal(in.Trigger)
		fields = append(fields, secrets.StoreField{Name: "trigger data", Text: string(rawTrigger)})
	}
	if strings.Contains(string(rawConfig), "{{") {
		for name, its := range in.NodeOutputs {
			js := make([]map[string]interface{}, 0, len(its))
			var bin strings.Builder
			for _, it := range its {
				js = append(js, it.JSON)
				binaryText(&bin, it)
			}
			raw, _ := json.Marshal(js)
			fields = append(fields, secrets.StoreField{Name: "expression over the output of the earlier node " + strconv.Quote(name), Text: string(raw) + bin.String()})
		}
	}
	return secrets.CheckStoreWrite(ctx, nodeType, fields...)
}

// binaryText appends an item's binary property names and bytes.
func binaryText(b *strings.Builder, it Item) {
	for name, data := range it.Binary {
		b.WriteString(name)
		b.WriteByte('\n')
		b.Write(data)
		b.WriteByte('\n')
	}
}
