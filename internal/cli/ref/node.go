package ref

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/spf13/cobra"
)

// refNodeCategoryOrder is the order `ref nodes` prints categories in;
// categories not listed follow alphabetically.
var refNodeCategoryOrder = []string{"trigger", "system", "core", "http", "data", "image", "vault", "db", "comm", "service", "applications", "agent", "org", "ai", "gemini", "instagram", "linkedin", "x", "tiktok", "hackernews", "producthunt", "people"}

// ── ref nodes ─────────────────────────────────────────────────────────────────

func refNodesCmd(d Deps) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "nodes",
		Short: "List all node types grouped by category",
		Long: `List every node type this binary registers, grouped by category.

Deprecated types (they still load, and fail at run time with a migration
hint) are hidden; --all shows them. Types marked [schema only] have no
hand-written guide — 'ref node <type>' still documents their fields and
operations from the schema.`,
		Run: func(cmd *cobra.Command, args []string) {
			d.WriteRefNodes(cmd.OutOrStdout(), d.Registry(), all)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Include deprecated node types")
	return cmd
}

// WriteRefNodes prints `ref nodes`: reg's types by category, deprecated ones only with all.
func (d Deps) WriteRefNodes(out io.Writer, reg *workflow.NodeTypeRegistry, all bool) {
	fmt.Fprintln(out, "╔══════════════════════════════════════════════════════════════╗")
	fmt.Fprintln(out, "║                  monoagentcli — node type reference                ║")
	fmt.Fprintln(out, "╚══════════════════════════════════════════════════════════════╝")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %-38s  %s\n", "Type", "Description")
	fmt.Fprintln(out, "  "+strings.Repeat("─", 70))

	byCategory := map[string][]string{}
	hidden := 0
	for _, t := range d.NodeTypes(reg) {
		if _, deprecated := workflow.IsDeprecatedNodeType(t); deprecated && !all {
			hidden++
			continue
		}
		cat := d.nodeRefCategory(t)
		byCategory[cat] = append(byCategory[cat], t)
	}
	for _, cat := range refCategoriesInOrder(byCategory) {
		fmt.Fprintf(out, "\n  ── %s %s\n", strings.ToUpper(cat), strings.Repeat("─", max(0, 60-len(cat))))
		for _, t := range d.refTypesInDocOrder(byCategory[cat]) {
			short := ""
			if doc := d.FindNodeDoc(t); doc != nil {
				short = doc.Short
			} else {
				short = strings.TrimSpace("[schema only] " + workflow.SchemaTitle(t))
			}
			fmt.Fprintf(out, "  %-38s  %s\n", t, short)
		}
	}
	fmt.Fprintln(out)
	if hidden > 0 {
		fmt.Fprintf(out, "  %d deprecated node types hidden — 'ref nodes --all' shows them.\n", hidden)
	}
	fmt.Fprintln(out, "  Run: monoagentcli ref node <type>   for full details on any node.")
}

// refCategoriesInOrder returns the categories present, known ones first in
// refNodeCategoryOrder, the rest alphabetically.
func refCategoriesInOrder(byCategory map[string][]string) []string {
	var cats []string
	known := map[string]bool{}
	for _, c := range refNodeCategoryOrder {
		known[c] = true
		if len(byCategory[c]) > 0 {
			cats = append(cats, c)
		}
	}
	var extra []string
	for c := range byCategory {
		if !known[c] {
			extra = append(extra, c)
		}
	}
	sort.Strings(extra)
	return append(cats, extra...)
}

// refTypesInDocOrder keeps hand-documented types in catalogue order (as the
// manual always listed them) and appends undocumented ones alphabetically.
func (d Deps) refTypesInDocOrder(types []string) []string {
	pos := map[string]int{}
	for i, doc := range d.NodeDocs() {
		pos[doc.Type] = i
	}
	out := append([]string(nil), types...)
	sort.SliceStable(out, func(i, j int) bool {
		pi, iok := pos[out[i]]
		pj, jok := pos[out[j]]
		switch {
		case iok && jok:
			return pi < pj
		case iok != jok:
			return iok
		default:
			return out[i] < out[j]
		}
	})
	return out
}

// ── ref node <type> ───────────────────────────────────────────────────────────

func refNodeCmd(d Deps) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "node <type>",
		Short: "Show detailed documentation for a specific node type",
		Long: `Show the reference for one node type: its credential platform, every
operation and, per operation, the fields it takes (label, type, required,
default, options, help) — all read from the node's schema — plus the
hand-written guide (config example, inputs, outputs, notes) when one exists.

--json prints the same page as a JSON document.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			page, err := d.BuildNodeRefPage(d.Registry(), args[0])
			if err != nil {
				return err
			}
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(page)
			}
			WriteNodeRefPage(cmd.OutOrStdout(), page)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print the page as JSON")
	return cmd
}

// WriteNodeRefPage prints a page as text: the hand-written prose (as the
// manual always showed it), then the schema-derived reference.
func WriteNodeRefPage(out io.Writer, p *NodeRefPage) {
	sep := strings.Repeat("═", 62)
	fmt.Fprintln(out, sep)
	fmt.Fprintf(out, "  NODE: %s\n", p.Type)
	fmt.Fprintf(out, "  Category: %s\n", p.Category)
	fmt.Fprintln(out, sep)
	if p.Short != "" {
		fmt.Fprintf(out, "\n  %s\n", p.Short)
	}
	if p.Deprecated && !strings.Contains(p.Short, "DEPRECATED") {
		fmt.Fprintf(out, "\n  DEPRECATED — fails at run time: %s\n", p.DeprecationHint)
	}
	if !p.HandWritten {
		fmt.Fprintln(out, "\n  No hand-written guide for this type; the reference below comes from its schema.")
	}
	writeIndented(out, "\n", "  ", p.Description)
	if p.ConfigExample != "" {
		fmt.Fprintln(out, "\n  Config example:")
		writeIndented(out, "", "    ", p.ConfigExample)
	}
	if p.Inputs != "" {
		fmt.Fprintf(out, "\n  Inputs:   %s\n", p.Inputs)
	}
	if p.Outputs != "" {
		fmt.Fprintf(out, "  Outputs:  %s\n", p.Outputs)
	}
	if p.Notes != "" {
		fmt.Fprintln(out, "\n  Notes:")
		writeIndented(out, "", "    ", p.Notes)
	}
	writeNodeRefSchema(out, p)
	fmt.Fprintln(out)
}

func writeNodeRefSchema(out io.Writer, p *NodeRefPage) {
	fmt.Fprintln(out, "\n  "+strings.Repeat("─", 60))
	if p.CredentialPlatform != "" {
		fmt.Fprintf(out, "  Credential platform: %s\n", p.CredentialPlatform)
	} else {
		fmt.Fprintln(out, "  Credential platform: none")
	}
	if len(p.Fields) == 0 && len(p.Operations) == 0 {
		fmt.Fprintln(out, "\n  Fields: none")
		return
	}
	if len(p.Fields) > 0 {
		heading := "Fields:"
		if p.OperationKey != "" {
			heading = "Fields (every operation):"
		}
		fmt.Fprintf(out, "\n  %s\n", heading)
		for _, f := range p.Fields {
			if f.Key == p.OperationKey {
				f.Options = nil // listed under Operations below
			}
			writeNodeRefField(out, "    ", f)
		}
	}
	if len(p.Operations) == 0 {
		return
	}
	fmt.Fprintf(out, "\n  Operations (%s):\n", p.OperationKey)
	for _, op := range p.Operations {
		name := op.Name
		if op.Default {
			name += "  (default)"
		}
		fmt.Fprintf(out, "    %s\n", name)
		if len(op.Fields) == 0 {
			fmt.Fprintln(out, "      no further fields")
		}
		for _, f := range op.Fields {
			writeNodeRefField(out, "      ", f)
		}
	}
}

// writeNodeRefField prints "key  Label · type · required · default=… ·
// options: a|b", then the placeholder, help and visibility condition.
func writeNodeRefField(out io.Writer, indent string, f NodeRefField) {
	parts := []string{}
	if f.Label != "" {
		parts = append(parts, f.Label)
	}
	if f.Type != "" {
		parts = append(parts, f.Type)
	}
	if f.Required {
		parts = append(parts, "required")
	}
	if f.Default != nil {
		parts = append(parts, "default="+refValue(f.Default))
	}
	if len(f.Options) > 0 {
		parts = append(parts, "options: "+strings.Join(f.Options, "|"))
	}
	fmt.Fprintf(out, "%s%-20s %s\n", indent, f.Key, strings.Join(parts, " · "))
	sub := indent + strings.Repeat(" ", 21)
	if f.Placeholder != "" {
		ph := f.Placeholder
		if !strings.HasPrefix(strings.ToLower(ph), "e.g.") {
			ph = "e.g. " + ph
		}
		fmt.Fprintf(out, "%s%s\n", sub, ph)
	}
	if f.Help != "" {
		writeIndented(out, "", sub, f.Help)
	}
	if f.ShownWhen != nil {
		fmt.Fprintf(out, "%sshown when %s is %s\n", sub, f.ShownWhen.Key, strings.Join(f.ShownWhen.Values, "|"))
	}
}

func refValue(v interface{}) string {
	if s, ok := v.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// writeIndented prints each line of text with indent, after lead; nothing
// when text is empty.
func writeIndented(out io.Writer, lead, indent, text string) {
	if text == "" {
		return
	}
	fmt.Fprint(out, lead)
	for _, line := range strings.Split(text, "\n") {
		fmt.Fprintf(out, "%s%s\n", indent, line)
	}
}
