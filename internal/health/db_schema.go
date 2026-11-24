package health

import (
	"context"
	"fmt"
)

// CheckDBSchema warns when the database was migrated by a newer monoagent
// than the running one (an older GUI-bundled CLI, a backup binary): it keeps
// working, but may not understand newer data.
const CheckDBSchema = "core.db.schema"

// CheckDBBackups shows what the snapshots taken before deleting a profile
// take on disk, and warns when they pass a gigabyte.
const CheckDBBackups = "core.db.backups"

const backupsWarnBytes = 1 << 30

func dbSchemaChecks() []Check {
	return []Check{{ID: CheckDBSchema, Group: GroupCore, Title: "Database schema version",
		DependsOn: []string{CheckDB}, Run: checkDBSchema},
		{ID: CheckDBBackups, Group: GroupCore, Title: "Pre-delete profile snapshots",
			DependsOn: []string{CheckDB}, Run: checkDBBackups}}
}

func checkDBBackups(_ context.Context, env *Env) Result {
	if env.ProfileBackupsSize == nil {
		return Result{Status: StatusSkip, Summary: "not available"}
	}
	n, bytes, err := env.ProfileBackupsSize()
	if err != nil {
		return Result{Status: StatusWarn, Summary: "cannot read the snapshots", Detail: err.Error()}
	}
	summary := fmt.Sprintf("%d snapshot(s), %.1f MB", n, float64(bytes)/(1<<20))
	if bytes > backupsWarnBytes {
		return Result{Status: StatusWarn, Summary: summary,
			Detail: "free the space with: monoagentcli db prune-backups (run it in your own terminal)"}
	}
	return Result{Status: StatusOK, Summary: summary}
}

func checkDBSchema(ctx context.Context, env *Env) Result {
	if env.SchemaAhead == nil {
		return Result{Status: StatusSkip, Summary: "not available"}
	}
	dbV, binV, ahead, err := env.SchemaAhead(ctx)
	if err != nil {
		return Result{Status: StatusWarn, Summary: "cannot read schema version", Detail: err.Error()}
	}
	if ahead {
		return Result{Status: StatusWarn,
			Summary: fmt.Sprintf("database was migrated by a newer monoagent (v%03d > v%03d)", dbV, binV),
			Detail:  "this monoagentcli is older than the database; update it with: monoagentcli update"}
	}
	return Result{Status: StatusOK, Summary: fmt.Sprintf("v%03d", dbV)}
}
