package n8nimport

// registerWithApp has the trigger.webhook cfg register itself with the
// vendor on activation, as n8n's trigger did, when the app's API package
// has a webhook registrar: register_with names it, register_config gets
// the n8n params it reads and credential_id the node's connection. It
// reports whether the app has one.
func registerWithApp(c *MapContext, cfg map[string]interface{}) bool {
	app, ok := lookupApp(c.Type)
	if !ok || app.RegisterWith == "" {
		return false
	}
	rc := map[string]interface{}{}
	for _, k := range app.RegisterParams {
		if c.Has(k) && !isEmptyValue(c.Params[k]) {
			rc[k] = c.Value(k)
		}
	}
	for _, k := range app.RegisterRequired {
		if _, ok := rc[k]; !ok {
			c.Warn(k, nil, "%s is not set: add it to the trigger's register_config before activating", k)
		}
	}
	cfg["register_with"] = app.RegisterWith
	if len(rc) > 0 {
		cfg["register_config"] = rc
	}
	if id := app.credential(c); id != "" {
		cfg["credential_id"] = id
	}
	return true
}
