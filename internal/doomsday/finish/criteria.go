package finish

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// A criterion as the charter stores it is three strings: the kind, the spec and the expected result. This file turns
// them into a Plan the harness executes, with these defaults (note 13.6 leaves the encoding of the CLI charter open):
//
//	command / output  spec is an argument vector split WITHOUT a shell (quotes group words, nothing expands; an unquoted
//	                  shell operator is refused). expected "exit:0" or "exit:0,2" accepts those exit codes and compares no
//	                  output; any other expected text is the exact stdout, compared byte for byte after one final newline
//	                  is dropped from each side, with exit code 0.
//	file              spec is a relative path inside the subject (no "..", links are not followed); expected is
//	                  "exists", "absent", "sha256:<hex>" or "size<=N".
//	test              not supported by this harness: its result is "error", which parks the finish (never a pass).
//	rubric            not a machine criterion: a person decides it.
type Plan struct {
	Kind   string
	Argv   []string
	Exits  []int  // accepted exit codes (command: [0] unless exit: is given)
	Stdout string // exact expected stdout, when Compare
	// Compare: stdout is compared (an output criterion, or a command with literal expected text).
	Compare bool
	Path    string // file: relative, clean
	Pred    string // file: exists, absent, sha256, size
	Arg     string // file: the digest or the size bound
}

// ErrCriterion: the criterion cannot be run as written. The harness turns it into an "error" result.
var ErrCriterion = errors.New("finish: the criterion cannot be run")

func badCrit(f string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrCriterion, fmt.Sprintf(f, a...))
}

// ParsePlan reads one machine criterion.
func ParsePlan(kind, spec, expected string) (Plan, error) {
	switch kind {
	case "command", "output":
		argv, err := SplitArgv(spec)
		if err != nil {
			return Plan{}, err
		}
		p := Plan{Kind: kind, Argv: argv, Exits: []int{0}}
		if codes, ok := strings.CutPrefix(expected, "exit:"); ok {
			if kind == "output" {
				return Plan{}, badCrit("an output criterion names the expected stdout, not exit codes")
			}
			p.Exits = nil
			for _, s := range strings.Split(codes, ",") {
				n, err := strconv.Atoi(strings.TrimSpace(s))
				if err != nil || n < 0 || n > 255 {
					return Plan{}, badCrit("exit code %q", s)
				}
				p.Exits = append(p.Exits, n)
			}
			return p, nil
		}
		if expected == "" && kind == "output" {
			return Plan{}, badCrit("an output criterion needs the expected stdout")
		}
		if expected != "" {
			p.Compare, p.Stdout = true, strings.TrimSuffix(expected, "\n")
		}
		return p, nil
	case "file":
		clean := path.Clean(spec)
		if spec == "" || path.IsAbs(spec) || clean == ".." || strings.HasPrefix(clean, "../") || strings.ContainsRune(spec, 0) {
			return Plan{}, badCrit("the path %q is not a relative path inside the subject", spec)
		}
		p := Plan{Kind: kind, Path: clean}
		switch {
		case expected == "exists" || expected == "absent":
			p.Pred = expected
		case strings.HasPrefix(expected, "sha256:") && len(expected) == len("sha256:")+64:
			p.Pred, p.Arg = "sha256", strings.ToLower(expected[len("sha256:"):])
		case strings.HasPrefix(expected, "size<="):
			if n, err := strconv.ParseInt(expected[len("size<="):], 10, 64); err != nil || n < 0 {
				return Plan{}, badCrit("size bound %q", expected)
			}
			p.Pred, p.Arg = "size", expected[len("size<="):]
		default:
			return Plan{}, badCrit("a file criterion expects exists, absent, sha256:<64 hex> or size<=N, not %q", expected)
		}
		return p, nil
	case "test":
		return Plan{}, badCrit("test criteria are not supported by this harness; use a command or output criterion")
	}
	return Plan{}, badCrit("kind %q is not a machine criterion", kind)
}

// SplitArgv splits a command line into words without a shell. Single quotes keep everything, double quotes keep
// everything but a backslash, a backslash outside quotes escapes the next byte. An unquoted shell operator or
// expansion (| & ; < > ` $ * ? newline) is refused: nothing would interpret it, so it would silently not mean what it says.
func SplitArgv(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	in, quote := false, byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case quote == '"':
			switch {
			case c == '"':
				quote = 0
			case c == '\\' && i+1 < len(s):
				i++
				cur.WriteByte(s[i])
			default:
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, in = c, true
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			in = true
		case c == ' ' || c == '\t':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		case strings.IndexByte("|&;<>`$*?\n\r\x00", c) >= 0:
			return nil, badCrit("the unquoted %q would need a shell; a criterion is an argument vector", string(c))
		default:
			cur.WriteByte(c)
			in = true
		}
	}
	if quote != 0 {
		return nil, badCrit("an unterminated quote")
	}
	if in {
		out = append(out, cur.String())
	}
	if len(out) == 0 || out[0] == "" {
		return nil, badCrit("the command is empty")
	}
	return out, nil
}
