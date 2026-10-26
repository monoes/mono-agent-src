package aitask

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/aitask"
)

// kindInfo is one entry of `aitask kinds --json`: {"kinds":[…]}. Fields are only ever added.
type kindInfo struct {
	Name           string       `json:"name"`
	Description    string       `json:"description"`
	DefaultAccess  string       `json:"defaultAccess"`
	UnattendedOK   bool         `json:"unattendedOk"`
	ContextClasses []string     `json:"contextClasses"`
	ResultSchema   resultSchema `json:"resultSchema"`
}

// resultSchema names the kind's result contract and the top-level keys it has.
type resultSchema struct {
	Name       string   `json:"name"`
	Required   []string `json:"required"`
	Properties []string `json:"properties"`
}

func describeKind(k aitask.Kind) kindInfo {
	rs := resultSchema{Name: k.Name, Required: []string{}, Properties: []string{}}
	if req, ok := k.Schema["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				rs.Required = append(rs.Required, s)
			}
		}
	}
	props, _ := k.Schema["properties"].(map[string]any)
	for p := range props {
		rs.Properties = append(rs.Properties, p)
	}
	sort.Strings(rs.Properties)
	return kindInfo{Name: k.Name, Description: k.Description, DefaultAccess: k.DefaultAccess, UnattendedOK: k.UnattendedOK,
		ContextClasses: append([]string{}, aitask.ContextSources...), ResultSchema: rs}
}

func (c *cli) kinds() *cobra.Command {
	return &cobra.Command{
		Use:   "kinds",
		Short: "List the kinds of task there are, with what each does (read-only; anyone may run it)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			list := []kindInfo{}
			var lines []string
			kinds := aitask.All()
			if api := c.remote(); api != nil { // client mode: the daemon's registry
				infos, err := api.Kinds(cmd.Context())
				if err != nil {
					return cliErr(err)
				}
				kinds = kinds[:0:0]
				for _, i := range infos {
					kinds = append(kinds, aitask.Kind{Name: i.Name, Description: i.Description, DefaultAccess: i.DefaultAccess,
						UnattendedOK: i.UnattendedOK, Schema: i.Schema})
				}
			}
			for _, k := range kinds {
				info := describeKind(k)
				list = append(list, info)
				lines = append(lines, fmt.Sprintf("%-20s %-9s %s", info.Name, info.DefaultAccess, info.Description))
			}
			return c.print(cmd, map[string]any{"kinds": list}, strings.Join(lines, "\n"))
		},
	}
}
