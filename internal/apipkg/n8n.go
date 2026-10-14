package apipkg

// N8nMapping says how one n8n node type maps onto a package. Plain data,
// so the n8n importer can read it without importing workflow types.
type N8nMapping struct {
	N8nType   string `json:"n8n_type"`
	PackageID string `json:"package_id"`
	// NodeType is the app.* node type to emit; empty when Extends is set.
	NodeType string `json:"node_type,omitempty"`
	// Extends is the native node type the package's ops run under.
	Extends string `json:"extends,omitempty"`
	// Trigger marks an n8n trigger node type. Triggers are not supported
	// yet: Supported is false and Operations is empty.
	Trigger   bool `json:"trigger,omitempty"`
	Supported bool `json:"supported"`
	// DefaultResource and DefaultOperations stand in for the n8n params n8n
	// leaves out of saved workflows when they hold their default value.
	DefaultResource string `json:"default_resource,omitempty"`
	// OperationParam is the n8n param holding the operation when it
	// isn't "operation" (the engine reads it as is).
	OperationParam string `json:"operation_param,omitempty"`
	// ResourceParam is the n8n param holding the resource when it isn\'t "resource".
	ResourceParam string `json:"resource_param,omitempty"`
	// VersionDefaults: typeVersion → param defaults for omitted params.
	VersionDefaults   map[string]map[string]any `json:"version_defaults,omitempty"`
	DefaultOperations map[string]string         `json:"default_operations,omitempty"`
	Credentials       []N8nCredential           `json:"credentials,omitempty"`
	// Operations is keyed "<resource>.<operation>" (just the operation when
	// the package has no resources), in n8n's values.
	Operations map[string]N8nOperation `json:"operations,omitempty"`
	// RegisterWith (trigger types) is the webhook registrar of the
	// package's webhook block: trigger.webhook's register_with. Its
	// register config keys are RegisterParams (n8n's trigger param names);
	// RegisterRequired are the required ones without a default.
	RegisterWith     string   `json:"register_with,omitempty"`
	RegisterParams   []string `json:"register_params,omitempty"`
	RegisterRequired []string `json:"register_required,omitempty"`
}

// N8nCredential maps an n8n credential type to a connection platform.
type N8nCredential struct {
	N8nType  string            `json:"n8n_type"`
	Platform string            `json:"platform"`
	FieldMap map[string]string `json:"field_map,omitempty"` // n8n field → ours
}

// N8nOperation is one supported (resource, operation).
type N8nOperation struct {
	Resource  string `json:"resource"`
	Operation string `json:"operation"`
	// ParamMap renames n8n params whose name differs from ours
	// (n8n → ours). Params not listed keep their n8n name.
	ParamMap map[string]string `json:"param_map,omitempty"`
	// Params are the op's field keys.
	Params []string `json:"params,omitempty"`
}

// N8nIndex maps every n8n node type the loaded packages declare
// (n8n_types and n8n_trigger_types) to its mapping.
func N8nIndex() map[string]N8nMapping {
	out := map[string]N8nMapping{}
	for _, p := range packages() {
		base := N8nMapping{PackageID: p.ID, NodeType: p.NodeTypeOf(), Extends: p.Extends}
		for i := range p.Credentials {
			c := &p.Credentials[i]
			for _, t := range append([]string{c.N8nCredential}, c.N8nCredentialAlt...) {
				if t != "" {
					base.Credentials = append(base.Credentials, N8nCredential{
						N8nType: t, Platform: c.EffectivePlatform(), FieldMap: c.N8nFieldMap,
					})
				}
			}
		}
		for _, t := range p.N8nTypes {
			m := base
			m.N8nType, m.Supported = t, true
			m.DefaultResource = p.DefaultResource()
			m.OperationParam = p.N8nOperationParam
			m.ResourceParam = p.N8nResourceParam
			m.VersionDefaults = p.N8nVersionDefaults
			m.DefaultOperations = map[string]string{}
			m.Operations = map[string]N8nOperation{}
			for _, o := range p.Operations {
				if _, ok := m.DefaultOperations[o.Resource]; !ok {
					m.DefaultOperations[o.Resource] = o.Operation
				}
				keys := make([]string, 0, len(o.Fields))
				for _, f := range o.Fields {
					keys = append(keys, f.Key)
				}
				m.Operations[o.Key()] = N8nOperation{
					Resource: o.Resource, Operation: o.Operation, ParamMap: o.N8nParamMap, Params: keys,
				}
			}
			out[t] = m
		}
		for _, t := range p.N8nTriggerTypes {
			m := base
			m.N8nType, m.Trigger, m.Supported = t, true, false
			if m.RegisterWith = p.webhookName(); m.RegisterWith != "" {
				for _, f := range p.Webhook.Fields {
					m.RegisterParams = append(m.RegisterParams, f.Key)
					if f.Required && f.Default == nil {
						m.RegisterRequired = append(m.RegisterRequired, f.Key)
					}
				}
			}
			out[t] = m
		}
	}
	return out
}
