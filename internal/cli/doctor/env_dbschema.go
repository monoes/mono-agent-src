package doctor

import (
	"github.com/monoes/mono-agent/internal/health"
	"github.com/monoes/mono-agent/internal/storage"
)

// addDBSchemaHook feeds the doctor's core.db.schema row: a read-only
// comparison of the database's newest recorded migration with this
// binary's newest embedded one.
func addDBSchemaHook(env *health.Env, db *storage.Database) {
	env.SchemaAhead = db.SchemaAhead
}
