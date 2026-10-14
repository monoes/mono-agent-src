package catalog

// The `db` entries of `ref commands`.
var dbCommandDocs = []cmdDoc{
	{
		Name:  "db migrate",
		Short: "Apply pending database migrations and bring every profile's folder layout and vault files up to date (idempotent)",
		Usage: "monoagentcli db migrate [--json]",
		Examples: []string{
			"monoagentcli db migrate",
			"monoagentcli --json db migrate",
		},
	},
}
