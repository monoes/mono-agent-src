package browserctl

import (
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/extension"
)

// The submit gate's page scripts (probe, hold, trigger) run in an isolated
// world of the tab's main frame: they share the page's DOM but not its
// JavaScript, so a page cannot patch FormData, JSON, crypto helpers or the
// prototypes they call to fake a digest or a check. Events they cause (the
// click, the submit) still reach the page and its submit guard.

// isolatedWorld names the world the gate's scripts run in.
const isolatedWorld = "monoagent-submit-gate"

// evalIsolated evaluates js in a new isolated world of p's main frame and
// returns its value.
func evalIsolated(p *extension.ExtensionPage, js string) (interface{}, error) {
	ctxID, err := newIsolatedWorld(p)
	if err != nil {
		return nil, err
	}
	return evalIn(p, ctxID, js)
}

// newIsolatedWorld opens an isolated world in p's main frame and returns its
// execution context, for scripts that must share state across evaluations.
func newIsolatedWorld(p *extension.ExtensionPage) (int, error) {
	tree, err := p.CDP("Page.getFrameTree", nil)
	if err != nil {
		return 0, err
	}
	ft, _ := tree["frameTree"].(map[string]interface{})
	frame, _ := ft["frame"].(map[string]interface{})
	frameID, _ := frame["id"].(string)
	if frameID == "" {
		return 0, errors.New("the page has no main frame to run the check in")
	}
	world, err := p.CDP("Page.createIsolatedWorld", map[string]interface{}{"frameId": frameID, "worldName": isolatedWorld})
	if err != nil {
		return 0, err
	}
	var ctxID int
	switch v := world["executionContextId"].(type) {
	case float64:
		ctxID = int(v)
	case int:
		ctxID = v
	}
	if ctxID == 0 {
		return 0, errors.New("the page did not open an isolated world for the check")
	}
	return ctxID, nil
}

// evalIn evaluates js in the isolated world ctxID and returns its value.
func evalIn(p *extension.ExtensionPage, ctxID int, js string) (interface{}, error) {
	res, err := p.CDP("Runtime.evaluate", map[string]interface{}{
		"expression": js, "contextId": ctxID, "returnByValue": true, "awaitPromise": true,
	})
	if err != nil {
		return nil, err
	}
	if ex, ok := res["exceptionDetails"].(map[string]interface{}); ok {
		text, _ := ex["text"].(string)
		if e, ok := ex["exception"].(map[string]interface{}); ok {
			if d, ok := e["description"].(string); ok {
				text = d
			}
		}
		return nil, fmt.Errorf("the page check failed: %s", text)
	}
	out, _ := res["result"].(map[string]interface{})
	return out["value"], nil
}
