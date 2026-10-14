// Package n8napps connects the n8n importer to the declarative API
// packages: every n8n node type a package declares (apipkg.N8nIndex)
// becomes an n8nimport.AppMapper. n8nimport does not import apipkg; this
// package registers an app source with it at init, so any binary that
// imports it (internal/noderegistry does) imports n8n app nodes as
// app.* / extended native nodes. The source is read on every conversion,
// so packages installed after start-up are picked up.
package n8napps

import (
	"fmt"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/apipkg"
	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/n8nimport"
)

func init() {
	n8nimport.RegisterAppSource(Mappers)
}

// Mappers converts the loaded API packages' n8n index into AppMappers,
// sorted by n8n type.
func Mappers() []n8nimport.AppMapper {
	idx := apipkg.N8nIndex()
	types := make([]string, 0, len(idx))
	for t := range idx {
		types = append(types, t)
	}
	sort.Strings(types)
	out := make([]n8nimport.AppMapper, 0, len(types))
	for _, t := range types {
		out = append(out, appMapper(idx[t]))
	}
	return out
}

func appMapper(m apipkg.N8nMapping) n8nimport.AppMapper {
	a := n8nimport.AppMapper{
		N8nTypes:          []string{m.N8nType},
		NodeType:          m.NodeType,
		Extends:           m.Extends,
		PackageID:         m.PackageID,
		Trigger:           m.Trigger || !m.Supported,
		DefaultResource:   m.DefaultResource,
		DefaultOperations: m.DefaultOperations,
		OperationParam:    m.OperationParam,
		ResourceParam:     m.ResourceParam,
		VersionDefaults:   m.VersionDefaults,
		RegisterWith:      m.RegisterWith,
		RegisterParams:    m.RegisterParams,
		RegisterRequired:  m.RegisterRequired,
	}
	if m.Extends != "" {
		a.NodeType = ""
	}
	for _, c := range m.Credentials {
		if a.CredentialPlatform == nil {
			a.CredentialPlatform = map[string]string{}
			a.CredentialHints = map[string]string{}
		}
		if _, dup := a.CredentialPlatform[c.N8nType]; dup {
			continue
		}
		a.CredentialPlatform[c.N8nType] = c.Platform
		a.CredentialHints[c.N8nType] = credentialHint(c)
	}
	if !a.Trigger {
		a.Operations = make(map[string]bool, len(m.Operations))
	}
	for key, op := range m.Operations {
		a.Operations[key] = true
		if len(op.ParamMap) == 0 {
			continue
		}
		if a.ParamMap == nil {
			a.ParamMap = map[string]map[string]string{}
		}
		a.ParamMap[key] = op.ParamMap
	}
	return a
}

// credentialHint is the report line saying how to create the connection
// an imported node needs: the connect command (with the umbrella's scope
// groups for Google and Microsoft credentials) and the n8n credential
// fields that become ours under another name.
func credentialHint(c apipkg.N8nCredential) string {
	hint := "monoagentcli connect " + c.Platform
	if cm, ok := connections.N8nCredentials[c.N8nType]; ok && cm.Umbrella != "" {
		hint = "monoagentcli connect " + cm.Umbrella
		if len(cm.ScopeGroups) > 0 {
			hint += " --scopes " + strings.Join(cm.ScopeGroups, ",")
		}
		if cm.Note != "" {
			hint += " (" + cm.Note + ")"
		}
	}
	var renames []string
	for n8n, ours := range c.FieldMap {
		if n8n != ours {
			renames = append(renames, fmt.Sprintf("%s → %s", n8n, ours))
		}
	}
	if len(renames) > 0 {
		sort.Strings(renames)
		hint += "; n8n credential fields: " + strings.Join(renames, ", ")
	}
	return hint
}
