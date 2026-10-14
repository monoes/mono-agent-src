package apipkg

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/workflow"
)

func init() {
	// Both hooks are lazy: nothing is loaded until a schema or platform is
	// looked up.
	workflow.RegisterSchemaProvider(schemaProvider)
	connections.AddPlatformSource(registerPlatforms)
}

// Register adds one node type per package that does not extend a native
// node. A type already in r is left alone.
func Register(r *workflow.NodeTypeRegistry) {
	for _, p := range packages() {
		t := p.NodeTypeOf()
		if t == "" || r.Has(t) || len(p.Operations) == 0 {
			continue // trigger-only packages register no action node
		}
		pkg := p
		r.Register(t, func() workflow.NodeExecutor { return &Node{pkg: pkg} })
	}
}

// registerPlatforms registers each package credential's connection
// platform (the first package to name a platform defines it).
func registerPlatforms() {
	done := map[string]bool{}
	for _, p := range packages() {
		for i := range p.Credentials {
			c := &p.Credentials[i]
			if c.ReusePlatform != "" || done[c.Platform] {
				continue
			}
			def, ok := platformDef(p, c)
			if !ok {
				continue
			}
			if connections.RegisterPlatform(def) == nil {
				done[c.Platform] = true
			}
		}
	}
}

// platformDef converts a credential to a connections platform. A
// credential whose only method is "none" has no platform.
func platformDef(p *Package, c *Credential) (connections.PlatformDef, bool) {
	name := c.Name
	if name == "" {
		name = p.Title
	}
	def := connections.PlatformDef{
		ID: c.Platform, Name: name, Category: "app", ConnectVia: "API",
		Fields: map[connections.AuthMethod][]connections.CredentialField{}, IconEmoji: "🔌",
	}
	fields := make([]connections.CredentialField, 0, len(c.Fields))
	for _, f := range c.Fields {
		fields = append(fields, connections.CredentialField{
			Key: f.Key, Label: f.Label, Secret: f.Secret, Required: f.Required, HelpText: f.Help,
		})
	}
	add := func(m connections.AuthMethod) {
		for _, x := range def.Methods {
			if x == m {
				return
			}
		}
		def.Methods = append(def.Methods, m)
		if m != connections.MethodOAuth {
			def.Fields[m] = fields
		}
	}
	for _, m := range c.Methods {
		switch m {
		case "api_key", "bearer", "client_credentials":
			add(connections.MethodAPIKey)
		case "basic":
			add(connections.MethodAppPass)
		case "oauth":
			if c.OAuth != nil {
				add(connections.MethodOAuth)
				def.OAuth = oauthConfig(c, fields)
			}
		case "oauth1":
			if c.OAuth != nil {
				// The flow asks for the consumer key/secret (and other
				// fields) and fills the token pair; pasting all four is the
				// api-key form.
				var form []connections.CredentialField
				for _, f := range fields {
					if f.Key != connections.OAuth1Token && f.Key != connections.OAuth1TokenSecret {
						form = append(form, f)
					}
				}
				def.Methods = append(def.Methods, connections.MethodOAuth1)
				def.Fields[connections.MethodOAuth1] = form
				def.OAuth1 = &connections.OAuth1Config{
					RequestTokenURL: c.OAuth.RequestTokenURL, AuthURL: c.OAuth.AuthURL,
					AccessTokenURL: c.OAuth.TokenURL, SignatureMethod: c.OAuth.SignatureMethod,
				}
				add(connections.MethodAPIKey)
			}
		}
	}
	return def, len(def.Methods) > 0
}

// oauthConfig is the connections OAuth config of credential c: its connect
// fields, templated per-tenant endpoints and metadata lookup.
func oauthConfig(c *Credential, fields []connections.CredentialField) *connections.OAuthConfig {
	o := c.OAuth
	cfg := &connections.OAuthConfig{
		AuthURL: o.AuthURL, TokenURL: o.TokenURL,
		Scopes: o.Scopes, ExtraParams: o.ExtraParams,
		TokenFields: o.TokenFields,
	}
	for _, k := range o.ConnectFields {
		for _, f := range fields {
			if f.Key == k {
				cfg.ConnectFields = append(cfg.ConnectFields, f)
			}
		}
	}
	if oauthTemplated(o) {
		cfg.Tailor = func(cfg connections.OAuthConfig, data map[string]interface{}) (connections.OAuthConfig, error) {
			return tailorOAuth(o, cfg, data)
		}
	}
	if o.Metadata != nil {
		platform := c.Platform
		cfg.AfterToken = func(ctx context.Context, data map[string]interface{}) error {
			vals, err := fetchMetadata(ctx, o.Metadata, data)
			if err != nil {
				return fmt.Errorf("credential %s: %w", platform, err)
			}
			for k, v := range vals {
				data[k] = v
			}
			return nil
		}
	}
	return cfg
}
