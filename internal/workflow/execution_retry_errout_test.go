package workflow

import (
	"context"
	"testing"
)

// A node that routes its failure to the "error" output (http.request on an
// HTTP 500, a failed command) is retried per retry_policy like one that
// returns an error, and routes the last attempt's error items when every
// attempt failed.
func TestExecuteWithRetry_ErrorOutputIsRetried(t *testing.T) {
	noSleep(t)
	errItem := []Item{NewItem(map[string]interface{}{"status": 500, "body": "boom"})}
	okItem := []Item{NewItem(map[string]interface{}{"status": 200})}
	calls := 0
	flaky := funcExecutor(func(context.Context, NodeInput) ([]NodeOutput, error) {
		calls++
		if calls <= 2 {
			return []NodeOutput{{Handle: "error", Items: errItem}}, nil
		}
		return []NodeOutput{{Handle: "main", Items: okItem}}, nil
	})
	out, err := executeWithRetry(context.Background(), flaky, NodeInput{}, map[string]interface{}{}, RetryPolicy{MaxRetries: 3, InitialDelay: 1})
	if err != nil || calls != 3 || len(out) != 1 || out[0].Handle != "main" {
		t.Fatalf("flaky: calls=%d out=%v err=%v; want success on the 3rd attempt", calls, out, err)
	}

	calls = 0
	down := funcExecutor(func(context.Context, NodeInput) ([]NodeOutput, error) {
		calls++
		return []NodeOutput{{Handle: "error", Items: errItem}}, nil
	})
	out, err = executeWithRetry(context.Background(), down, NodeInput{}, map[string]interface{}{}, RetryPolicy{MaxRetries: 2, InitialDelay: 1})
	if err != nil || calls != 3 || len(out) != 1 || out[0].Handle != "error" {
		t.Fatalf("down: calls=%d out=%v err=%v; want 3 attempts, then the error items routed", calls, out, err)
	}

	calls = 0
	if _, err := executeWithRetry(context.Background(), down, NodeInput{}, map[string]interface{}{}, RetryPolicy{}); err != nil || calls != 1 {
		t.Fatalf("no policy: calls=%d err=%v; want one attempt", calls, err)
	}
}
