package workflow

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/vault"
)

// injectCredential merges credential data into config if credential_id is
// present, and returns the connection it merged (nil for none): where its
// secret may go is checked once the config resolves (checkCredentialUse).
// GetOrResolve looks up the connections table by id or platform name and
// auto-refreshes expired OAuth tokens. It is scoped to the execution's
// profile so a workflow under profile B never resolves profile A's
// credentials.
func (r *runner) injectCredential(node WorkflowNode, config map[string]interface{}) *CredentialUse {
	return mergeCredential(r.ctx, r.connStore, r.logger, node.Type, config)
}

// checkCredentialUse refuses resolved when it points use's secret elsewhere
// than the connection names (CheckCredentialDestinations); a run of a
// workflow the user never approved (AIAuthoredFrom) sets no host of its own.
func checkCredentialUse(ctx context.Context, executor NodeExecutor, use *CredentialUse, stored, resolved map[string]interface{}) error {
	return CheckCredentialDestinations(executor, use, !AIAuthoredFrom(ctx), stored, resolved)
}

// ResolveCredentials returns a copy of cfg with its credential_id merged in
// the way a run does before Execute (connection fields at the top level and
// under "credential"), scoped to the profile on ctx. Long-running trigger
// sources (queue/DB consumers) use it when they activate. A credential that
// does not resolve is logged and cfg is returned as it is.
func (e *WorkflowEngine) ResolveCredentials(ctx context.Context, nodeType string, cfg map[string]interface{}) map[string]interface{} {
	out, _ := e.resolveCredentials(ctx, nodeType, cfg)
	return out
}

// ResolveTriggerCredentials is ResolveCredentials for the trigger node of
// workflow w, whose destination keys are keys: a config that points the
// connection's secret at a host the connection doesn't name is refused
// (CheckDestinationKeys) unless the user approved w's definition.
func (e *WorkflowEngine) ResolveTriggerCredentials(ctx context.Context, w *Workflow, nodeType string, keys [][]string, cfg map[string]interface{}) (map[string]interface{}, error) {
	out, use := e.resolveCredentials(ctx, nodeType, cfg)
	if len(keys) == 0 {
		return out, nil
	}
	if use != nil {
		approved, _ := operatorApproved(ctx, e.store, w)
		if err := CheckDestinationKeys(keys, use, approved, out, out); err != nil {
			return nil, fmt.Errorf("%s: %w", nodeType, err)
		}
	}
	// Under agent access a consumer sends nothing the run read from the vault
	// to a host the grant does not name (CheckSendDestinations).
	if err := CheckSendDestinations(ctx, nodeType, keys, out); err != nil {
		return nil, fmt.Errorf("%s: %w", nodeType, err)
	}
	return out, nil
}

func (e *WorkflowEngine) resolveCredentials(ctx context.Context, nodeType string, cfg map[string]interface{}) (map[string]interface{}, *CredentialUse) {
	out := make(map[string]interface{}, len(cfg))
	for k, v := range cfg {
		out[k] = v
	}
	return out, mergeCredential(ctx, e.connStore, e.logger, nodeType, out)
}

func mergeCredential(ctx context.Context, connStore *connections.Store, logger zerolog.Logger, nodeType string, config map[string]interface{}) *CredentialUse {
	credIDRaw, ok := config["credential_id"]
	if !ok {
		return nil
	}
	credID, ok := credIDRaw.(string)
	if !ok || credID == "" {
		return nil
	}
	injected := false
	var use *CredentialUse

	if connStore != nil {
		profile, perr := vault.ProfileIDFromContext(ctx)
		var conn *connections.Connection
		err := perr
		if perr == nil {
			conn, err = connStore.GetOrResolve(ctx, credID, profile)
		}
		if err != nil {
			logger.Warn().Err(err).
				Str("credential_id", credID).
				Msg("connections lookup failed")
			// conn is nil; injected stays false, miss handling below runs
		} else if conn != nil {
			// Connection fields fill only the keys the node's own config
			// leaves unset (absent, null or ""), so a connection's "url"
			// cannot replace the node's url; config["credential"] keeps
			// the full data.
			for k, v := range conn.Data {
				if isUnsetConfigValue(config[k]) {
					config[k] = v
				}
			}
			config["credential"] = conn.Data
			// Pinned to the connection this run chose: a send's card is
			// approved later, and must not choose again by name.
			if conn.ID != "" {
				config["credential_id"] = conn.ID
			}
			// Which service the connection is for, for nodes that serve
			// several (agent.ask with a model vendor's key).
			if isUnsetConfigValue(config["credential_platform"]) && conn.Platform != "" {
				config["credential_platform"] = conn.Platform
			}
			injected = true
			name := conn.Label
			if name == "" {
				name = credID
			}
			use = &CredentialUse{Name: name, Platform: conn.Platform, Data: conn.Data}
		}
	}

	// For browser platform nodes (instagram, gemini, linkedin, etc.),
	// the credential_id is just the session username. Inject it as
	// "username" so BrowserNode can resolve the session.
	if !injected {
		browserPlatforms := map[string]bool{
			"instagram": true, "gemini": true, "linkedin": true,
			"x": true, "tiktok": true, "telegram": true,
		}
		parts := strings.SplitN(nodeType, ".", 2)
		if len(parts) == 2 && browserPlatforms[parts[0]] {
			config["username"] = credID
			injected = true
		}
	}

	if !injected {
		// Miss on every path. Include the execution's profile in
		// the log so cross-profile resolution failures are
		// diagnosable.
		profileLog, _ := vault.ProfileIDInContext(ctx)
		logger.Warn().
			Str("credential_id", credID).
			Str("profile", profileLog).
			Msg("credential not resolved for profile")
	}
	return use
}

func isUnsetConfigValue(v interface{}) bool {
	s, isStr := v.(string)
	return v == nil || (isStr && s == "")
}

// checkStoredFields refuses resolved when its credential_id, or a field
// executor names a vault entry by (LiteralConfigResolver), isn't the value
// stored holds as written: an expression produced it, from the run's data
// or otherwise. So does a reference an expression put in the text of a field
// executor resolves references inside (SecretTextResolver). It runs before
// references resolve.
func checkStoredFields(executor NodeExecutor, stored, resolved map[string]interface{}) error {
	if t, ok := executor.(SecretTextResolver); ok {
		for _, f := range t.SecretTextFields() {
			if path, found := secrets.DataReference(stored[f], resolved[f], true); found {
				if path != "" {
					f += "." + path
				}
				return fmt.Errorf("%q: %w: only one written as it is in the workflow resolves", f, secrets.ErrDataReference)
			}
		}
	}
	fields := []string{"credential_id"}
	if l, ok := executor.(LiteralConfigResolver); ok {
		fields = append(fields, l.LiteralConfigFields()...)
	}
	for _, f := range fields {
		v, set := resolved[f]
		if !set || v == nil || v == "" {
			continue
		}
		s, isText := stored[f].(string)
		if (!isText || !strings.Contains(s, "{{")) && reflect.DeepEqual(stored[f], v) {
			continue
		}
		what := "the vault entry's name"
		switch f {
		case "credential_id":
			what = "the saved connection"
		case "url": // mcp.call's: where the vault entry's headers go
			what = "the address its headers go to"
		case "command", "args": // mcp.call's: the program the vault entry's env goes to
			what = "the program its env goes to"
		case "username": // a browser node's: the session it signs in with
			what = "the account it acts as"
		}
		hint := ""
		if f == "url" {
			hint = `; write the address as it is, or leave "url" out to use the vault entry's`
		}
		return fmt.Errorf("%q: %s must be written in the workflow, not taken from its data%s", f, what, hint)
	}
	return nil
}
