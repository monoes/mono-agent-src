package n8nimport

import (
	"strings"

	dbnodes "github.com/monoes/mono-agent/internal/nodes/db"
)

// n8n database and queue nodes converted through internal/nodes/db's
// parameter tables (Oracle, SQL Server, Snowflake, TimescaleDB, CrateDB,
// QuestDB, Kafka, RabbitMQ, AMQP, MQTT, LDAP). Postgres, MySQL, MongoDB and
// Redis keep their own mappers in map_db.go. The queue triggers are left
// unmapped until the engine registers those trigger types.
func init() {
	for n8nType, m := range dbnodes.N8nNodeMappings {
		if m.Trigger || handMappedDB[n8nType] {
			continue
		}
		spec, defaults := dbParamSpec(m), m.Defaults
		platform := m.MonoType[strings.Index(m.MonoType, ".")+1:]
		Register(n8nType, func(c *MapContext) Mapping {
			mp := mapWithParams(c, spec)
			if mp.Config == nil {
				return mp
			}
			// Our defaults fill only what the node didn't set (n8n leaves
			// defaults out of exports); set values win.
			for k, v := range defaults {
				if _, set := mp.Config[k]; !set {
					mp.Config[k] = v
				}
			}
			if len(c.Credentials) > 0 {
				mp.Config["credential_id"] = c.Credential(platform)
			}
			return mp
		})
	}
}

// dbParamSpec turns a db mapping into the importer's parameter spec; its
// operation renames are option-value rewrites (defaults are filled in by
// the mapper, after the node's own values).
func dbParamSpec(m dbnodes.N8nNodeMapping) paramSpec {
	spec := paramSpec{Type: m.MonoType, Params: m.Params}
	if len(m.Operations) > 0 {
		spec.Values = map[string]map[string]string{"operation": m.Operations}
	}
	return spec
}

// handMappedDB are the db types with their own mappers in map_db.go.
var handMappedDB = map[string]bool{
	"n8n-nodes-base.postgres": true, "n8n-nodes-base.mySql": true,
	"n8n-nodes-base.mongoDb": true, "n8n-nodes-base.redis": true,
}
