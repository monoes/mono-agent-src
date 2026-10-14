package catalog

// The `task os` entries of `ref commands`, beside commands_tasks.go's.
var taskOSCommandDocs = []cmdDoc{
	{
		Name:  "task os",
		Short: "The macOS menu: select text in any app, then Services, Add to MonoAgent Tasks",
		Usage: "monoagentcli [--profile P] task os install|status|uninstall [--dest DIR]",
		Flags: `  install     Add the menu item for one profile (run once per profile)
  status      The installed menus: current, stale, or profile_gone
  uninstall   Remove one profile's menu
  --dest DIR  The Services folder (default ~/Library/Services)`,
		Examples: []string{
			"monoagentcli --profile Work task os install",
			"monoagentcli task os status",
		},
	},
	{
		Name:  "task os install",
		Short: `Add "Add to MonoAgent Tasks: <profile>" to the macOS Services menu, filing into one profile (the operator only)`,
		Usage: "monoagentcli [--profile P] task os install [--force] [--dest DIR]",
		Flags: `  --force     Replace a bundle of the same name that this command did not write
  --dest DIR  The Services folder (default ~/Library/Services)`,
		Examples: []string{
			"monoagentcli task os install                  # the active profile",
			"monoagentcli --profile Work task os install   # once per profile",
		},
	},
	{
		Name:     "task os status",
		Short:    "The installed Add to MonoAgent Tasks menus: their profile and whether each is current",
		Usage:    "monoagentcli task os status [--dest DIR]",
		Examples: []string{"monoagentcli --json task os status"},
	},
	{
		Name:     "task os uninstall",
		Short:    "Remove a profile's Add to MonoAgent Tasks menu (a deleted profile is named by its id; the operator only)",
		Usage:    "monoagentcli [--profile P] task os uninstall [--dest DIR]",
		Examples: []string{"monoagentcli --profile Work task os uninstall"},
	},
}
