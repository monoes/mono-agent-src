package main

import "strconv"

// Publication history is owned by the CLI and scoped by rawCLI to the active profile.
func (a *App) ListPublications(search, platform, kind, workflow, agent, since, until string, limit, offset int) string {
	args := []string{"publication", "list", "--limit", strconv.Itoa(limit), "--offset", strconv.Itoa(offset)}
	for _, filter := range []struct{ flag, value string }{
		{"--search", search}, {"--platform", platform}, {"--kind", kind},
		{"--workflow", workflow}, {"--agent", agent}, {"--since", since}, {"--until", until},
	} {
		if filter.value != "" {
			args = append(args, filter.flag, filter.value)
		}
	}
	return a.rawCLI(summaryCLITimeout, args...)
}

func (a *App) GetPublication(id string) string {
	return a.rawCLI(summaryCLITimeout, "publication", "get", id)
}

func (a *App) GetPublicationStats() string {
	return a.rawCLI(summaryCLITimeout, "publication", "stats")
}
