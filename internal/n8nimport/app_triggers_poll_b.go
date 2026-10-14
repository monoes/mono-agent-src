package n8nimport

import "fmt"

// Poll plans of the time-tracking, Databricks and Venafi triggers; the
// table is in app_triggers_poll.go.

func planClockify(c *MapContext, p *pollPlan) string {
	p.Resource, p.Operation = "timeEntry", "getAll"
	c.Ignore("watchField") // only "new time entry" exists
	p.Config["workspaceId"] = c.Value("workspaceId")
	p.Config["returnAll"] = true
	p.Config["filters"] = map[string]interface{}{
		"start": sinceExpr(p.Lookback, rfc3339Z), "end": nowRFC3339Z, "hydrated": true, "in-progress": false,
	}
	p.Dedupe = "={{ $json.id }}"
	return ""
}

func planToggl(c *MapContext, p *pollPlan) string {
	if ev := c.Literal("event", "newTimeEntry"); ev != "newTimeEntry" {
		return fmt.Sprintf("event %q is not known", ev)
	}
	p.Resource, p.Operation = "timeEntry", "getAll"
	p.Config["returnAll"] = true
	p.Config["filters"] = map[string]interface{}{"startDate": sinceExpr(p.Lookback, rfc3339Z), "endDate": nowRFC3339Z}
	p.Dedupe = "={{ $json.id }}"
	return ""
}

func planVenafiDatacenter(c *MapContext, p *pollPlan) string {
	if on := c.Literal("triggerOn", "certificateExpired"); on != "certificateExpired" {
		return fmt.Sprintf("triggerOn %q is not known", on)
	}
	p.Resource, p.Operation = "certificate", "getMany"
	p.Config["returnAll"] = true
	p.Config["filters"] = map[string]interface{}{"ValidToGreater": sinceExpr(p.Lookback, rfc3339Z), "ValidToLess": nowRFC3339Z}
	p.Dedupe = "={{ $json.Guid }}"
	return ""
}

// planDatabricks lists a job's runs or a pipeline's updates. n8n emits
// one event per state change it saw; here a run or update passes once per
// identity below, so the subscribed events decide what identifies it.
func planDatabricks(c *MapContext, p *pollPlan) string {
	c.Ignore("servicePrincipalNotice", "continuousPipelineNotice")
	if c.Bool("simplify", true) {
		c.Warn("simplify", true, "items are the Databricks API objects, not n8n's simplified event items")
	}
	if c.Literal("resource", "job") == "pipeline" {
		return planDatabricksPipeline(c, p)
	}
	p.Resource, p.Operation = "job", "listRuns"
	p.Config["jobId"] = c.rlValue("jobId")
	p.Config["returnAll"] = true
	started, succeeded, failed := databricksEvents(c, "runStarted", "runSucceeded", "runFailed")
	filters := map[string]interface{}{"startTimeFrom": sinceExpr(p.Lookback, rfc3339Z)}
	switch {
	case started && (succeeded || failed):
		p.Dedupe = "={{ $json.run_id }}|{{ $json.end_time }}"
		c.Note("a run passes when first seen and again when it ends")
	case started:
		p.Dedupe = "={{ $json.run_id }}"
	default:
		filters["completedOnly"] = true
		p.Dedupe = "={{ $json.run_id }}"
	}
	if succeeded != failed {
		c.Warn("events", c.Raw("events"), "successful and failed runs both pass: check state.result_state after this node")
	}
	p.Config["filters"] = filters
	return ""
}

func planDatabricksPipeline(c *MapContext, p *pollPlan) string {
	p.Resource, p.Operation = "pipeline", "listUpdates"
	p.Config["pipelineId"] = c.rlValue("pipelineId")
	started, completed, failed := databricksEvents(c, "updateStarted", "updateCompleted", "updateFailed")
	p.Dedupe = "={{ $json.update_id }}"
	if completed || failed {
		p.Dedupe = "={{ $json.update_id }}|{{ $json.state }}"
		c.Warn("events", c.Raw("events"), "every state an update is seen in passes (RUNNING, COMPLETED, FAILED, …): check state after this node")
	} else if !started {
		return "no events are selected"
	}
	return ""
}

// databricksEvents reads the trigger's events (n8n's default: the two
// end events) and reports which of the three kinds are subscribed.
func databricksEvents(c *MapContext, start, success, failure string) (started, succeeded, failed bool) {
	events := []string{success, failure}
	switch raw := c.Raw("events").(type) {
	case nil:
	case []interface{}:
		events = events[:0]
		for _, e := range raw {
			events = append(events, fmt.Sprint(e))
		}
	default:
		c.Warn("events", raw, "is not a list of events; the importer used %s and %s", success, failure)
	}
	return contains(events, start), contains(events, success), contains(events, failure)
}
