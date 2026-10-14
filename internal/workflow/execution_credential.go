package workflow

import (
	"context"
	"strings"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/vault"
)

// injectCredential merges credential data into config if credential_id is
// present. GetOrResolve looks up the connections table by id or platform
// name and auto-refreshes expired OAuth tokens. It is scoped to the
// execution's profile so a workflow under profile B never resolves profile
// A's credentials.
func (r *runner) injectCredential(node WorkflowNode, config map[string]interface{}) {
	mergeCredential(r.ctx, r.connStore, r.logger, node.Type, config)
}

// ResolveCredentials returns a copy of cfg with its credential_id merged in
// the way a run does before Execute (connection fields at the top level and
// under "credential"), scoped to the profile on ctx. Long-running trigger
// sources (queue/DB consumers) use it when they activate. A credential that
// does not resolve is logged and cfg is returned as it is.
func (e *WorkflowEngine) ResolveCredentials(ctx context.Context, nodeType string, cfg map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(cfg))
	for k, v := range cfg {
		out[k] = v
	}
	mergeCredential(ctx, e.connStore, e.logger, nodeType, out)
	return out
}

func mergeCredential(ctx context.Context, connStore *connections.Store, logger zerolog.Logger, nodeType string, config map[string]interface{}) {
	credIDRaw, ok := config["credential_id"]
	if !ok {
		return
	}
	credID, ok := credIDRaw.(string)
	if !ok || credID == "" {
		return
	}
	injected := false

	if connStore != nil {
		conn, err := connStore.GetOrResolve(ctx, credID, vault.ProfileIDFromContext(ctx))
		if err != nil {
			logger.Warn().Err(err).
				Str("credential_id", credID).
				Msg("connections lookup failed")
			// conn is nil; injected stays false, miss handling below runs
		} else if conn != nil {
			// Merge all connection Data fields directly into config.
			for k, v := range conn.Data {
				config[k] = v
			}
			config["credential"] = conn.Data
			injected = true
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
		logger.Warn().
			Str("credential_id", credID).
			Str("profile", vault.ProfileIDFromContext(ctx)).
			Msg("credential not resolved for profile")
	}
}
