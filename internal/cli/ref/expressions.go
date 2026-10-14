package ref

import (
	"fmt"

	"github.com/spf13/cobra"
)

// ── ref expressions ───────────────────────────────────────────────────────────

func refExpressionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "expressions",
		Short: "Template expression syntax and built-in functions",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Print(`
╔══════════════════════════════════════════════════════════════╗
║              monoagentcli — expression / template reference        ║
╚══════════════════════════════════════════════════════════════╝

SYNTAX
  Config string values may contain Go template expressions: {{ ... }}
  Strings with no {{ }} are returned as-is (no parsing overhead).

BUILT-IN VARIABLES
  $json                       current item's JSON map
  $json.field_name            access a field on the current item
  $node["NodeName"].json.f    access a previous node's first output item
  $node["NodeName"].all       every output item of a previous node (a list;
                              .items is the same list)
  $items["NodeName"]          same list as $node["NodeName"].all
  $workflow.id                workflow ID string
  $execution.id               execution run ID string
  $env.MY_VAR                 OS environment variable; gated — resolves
                              to empty unless MONOAGENT_ALLOW_ENV_TEMPLATES=1
  $now                        current UTC time; prints as RFC3339, and has
                              time methods: $now.Format "2006-01-02", $now.Unix
  $today                      midnight UTC of the current day (like $now)
  $trigger.field              the execution's trigger data
  $vars.name                  workflow variables, when the caller sets them
                              (empty otherwise; never holds secrets)
  $runIndex                   loop iteration number, 0 outside a loop
  $prevNode                   name of the node that fed this one, when known

ENCODING
  json <value>                marshal Go value to JSON string
  jsonParse <string>          parse JSON string to Go value

TYPE CONVERSIONS
  toString <v>                convert any value to string
  toInt <v>                   convert to int64
  toFloat <v>                 convert to float64
  toBool <v>                  convert to bool

STRING OPERATIONS
  upper <s>                   uppercase
  lower <s>                   lowercase
  trim <s>                    trim whitespace
  trimLeft <cutset> <s>       trim left chars
  trimRight <cutset> <s>      trim right chars
  split <sep> <s>             split string → []string
  join <sep> <parts>          join []string → string
  replace <old> <new> <s>     replace all occurrences

MAP HELPERS
  hasKey <map> <key>          returns bool
  default <def> <v>           return def if v is nil or empty string

COLLECTION HELPERS
  len <v>                     length of string, array, or map
  index <arr> <n>             get element at index n (0-based)
                              works with []interface{}, []map[string]interface{},
                              and map[string]interface{}

TIME
  now                         current UTC time as RFC3339 string

ARITHMETIC  (all operate on float64)
  add <a> <b>
  sub <a> <b>
  mul <a> <b>
  div <a> <b>                 returns error on division by zero

EXAMPLES
  {{ $json.title }}                          — field access
  {{ upper $json.title }}                    — function call
  {{ index $json.titles 0 }}                 — first element of an array
  {{ replace "foo" "bar" $json.text }}       — string replace
  {{ json $json.items }}                     — marshal to JSON
  {{ default "n/a" $json.optional_field }}   — default value
  {{ $node["RSS Feed"].json.title }}         — previous node output
  {{ len $items["RSS Feed"] }}               — how many items a node output
  {{ range $node["RSS Feed"].all }}{{ .title }}, {{ end }}
                                             — every item of a node
  {{ $now.Format "2006-01-02" }}             — today's date
  {{ $env.SLACK_WEBHOOK }}                   — env variable
  {{ add $json.count 1.0 }}                  — arithmetic

TIPS
  - Config values that evaluate to a JSON array or object are automatically
    parsed back to native types so downstream nodes receive the correct type.
  - Use core.set to precompute values before passing them to API nodes.
  - The "replace" function is useful for reformatting Gemini output:
      {{ replace "▬▬▬▬▬▬▬" "\n\n▬▬▬▬▬▬▬\n\n" $json.response_text }}

`)
		},
	}
}
