package node

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	httpnodes "github.com/monoes/mono-agent/internal/nodes/http"
)

// newNodeCurlCmd converts a curl command line into an http.request node
// config. Flag parsing is off so the curl options reach the parser as is;
// global flags given before the curl words are parsed here.
func (d Deps) newNodeCurlCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "curl <curl command | ->",
		Short: "Turn a curl command into an http.request node config",
		Long: `Reads a curl command line (as copied from browser dev tools or API docs) and
prints the equivalent http.request node config as JSON. Pass the command as
one quoted argument, as separate arguments, or "-" to read it from stdin.
Things that could not be carried over are listed on stderr.`,
		Example: `  monoagentcli node curl 'curl -X POST https://api.example.com/items -H "Content-Type: application/json" -d "{\"name\":\"x\"}"'
  pbpaste | monoagentcli node curl -`,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			args, err := parseLeadingGlobalFlags(cmd, args)
			if err != nil {
				return d.InvalidInput("%v", err)
			}
			if len(args) == 0 || (len(args) == 1 && (args[0] == "-h" || args[0] == "--help")) {
				return cmd.Help()
			}
			command, err := d.curlCommandFromArgs(args, cmd.InOrStdin())
			if err != nil {
				return err
			}
			req, err := httpnodes.ParseCurl(command)
			if err != nil {
				return d.InvalidInput("%v", err)
			}
			out, err := json.MarshalIndent(req.NodeConfig(), "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			for _, n := range req.Notes {
				fmt.Fprintln(cmd.ErrOrStderr(), "note: "+n)
			}
			return nil
		},
	}
}

// parseLeadingGlobalFlags parses the global flags at the front of args and
// returns the rest. With flag parsing off, cobra hands this command every
// flag of the line, so `--profile p --json node curl -` arrives as
// "--profile p --json -". Parsing stops at the first word that is not a
// global flag ("curl", "-", a URL or a curl option).
func parseLeadingGlobalFlags(cmd *cobra.Command, args []string) ([]string, error) {
	fs := cmd.InheritedFlags()
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "-" || !strings.HasPrefix(a, "-") {
			break
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		f := fs.Lookup(name)
		if !strings.HasPrefix(a, "--") {
			f = nil
			if len(name) == 1 {
				f = fs.ShorthandLookup(name)
			}
		}
		if f == nil {
			break
		}
		n := 1
		if !hasValue && f.NoOptDefVal == "" {
			n = 2
		}
		if i+n > len(args) {
			break
		}
		i += n
	}
	if err := fs.Parse(args[:i]); err != nil {
		return nil, err
	}
	return args[i:], nil
}

// curlCommandFromArgs returns the curl command: stdin for "-", the single
// argument as given, or several arguments re-quoted so the parser sees the
// words the shell already split.
func (d Deps) curlCommandFromArgs(args []string, stdin io.Reader) (string, error) {
	if len(args) == 1 && args[0] == "-" {
		b, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		if strings.TrimSpace(string(b)) == "" {
			return "", d.InvalidInput("no curl command on stdin")
		}
		return string(b), nil
	}
	if len(args) == 1 {
		return args[0], nil
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " "), nil
}
