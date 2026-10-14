package n8nimport

func init() {
	Register("n8n-nodes-base.executeWorkflowTrigger", mapSubWorkflowTrigger)
	Register("n8n-nodes-base.errorTrigger", mapErrorTrigger)
	Register("n8n-nodes-base.workflowTrigger", mapWorkflowTrigger)
	Register("n8n-nodes-base.simulateTrigger", mapManual)
	Register("n8n-nodes-base.timeSaved", mapPassThrough)
	Register("n8n-nodes-base.simulate", mapPassThrough)
	Register("n8n-nodes-base.executionData", mapExecutionData)
}

// mapPassThrough maps helper nodes with no effect on the items.
func mapPassThrough(c *MapContext) Mapping {
	for k := range c.Params {
		c.Ignore(k)
	}
	return Mapping{Type: "core.merge", Config: map[string]interface{}{}}
}

// mapSubWorkflowTrigger: the items a parent workflow passes (core.execute_workflow)
// start the run at trigger.execute_workflow, as in n8n.
func mapSubWorkflowTrigger(c *MapContext) Mapping {
	c.Ignore("inputSource", "workflowInputs", "jsonExample")
	if len(c.paramList("workflowInputs.values")) > 0 {
		c.Note("the declared inputs are not checked: the run gets the items the caller passes")
	}
	c.Note("runs when another workflow calls it, or with `monoagentcli workflow run <id> --input '{…}'`")
	return Mapping{Type: "trigger.execute_workflow", Config: map[string]interface{}{}}
}

// mapErrorTrigger: the run starts with the failed execution's
// {execution, workflow} item, as n8n's Error Trigger does.
func mapErrorTrigger(c *MapContext) Mapping {
	c.Note("runs when a workflow whose settings.error_workflow names this workflow fails " +
		"(or for every workflow: monoagentcli config set workflow.error_workflow <id>)")
	return Mapping{Type: "trigger.error", Config: map[string]interface{}{}}
}

func mapWorkflowTrigger(c *MapContext) Mapping {
	c.Ignore("events")
	c.Warn("events", c.Raw("events"), "mono-agent has no workflow activation events: this workflow now starts manually")
	return Mapping{Type: "trigger.manual", Config: map[string]interface{}{}}
}

func mapExecutionData(c *MapContext) Mapping {
	c.Ignore("dataToSave")
	if c.Has("dataToSave") {
		c.Warn("dataToSave", c.Raw("dataToSave"), "custom execution data is not saved; the items pass on unchanged")
	}
	return Mapping{Type: "core.merge", Config: map[string]interface{}{}}
}
