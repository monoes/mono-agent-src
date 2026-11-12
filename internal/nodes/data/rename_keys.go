package data

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	"github.com/monoes/mono-agent/internal/workflow"
)

// RenameKeysNode renames item keys, by explicit pairs and/or by regular
// expression. Type: "data.rename_keys" (n8n: Rename Keys).
//
// Config:
//
//	keys                 list of {current_key, new_key}; dot paths reach nested keys
//	regex_replacements   list of {search_regex, replace_regex, case_insensitive, depth}
//	                     depth: -1 = every level (default), 0 = top level only
//
// Pairs are applied first, in order, then each regex replacement. A pair with
// an empty or identical name, or whose current key is missing, is skipped.
type RenameKeysNode struct{}

func (n *RenameKeysNode) Type() string { return "data.rename_keys" }

// PublishesToThirdParty: data.rename_keys sends nothing off this machine (issue #1418).
func (RenameKeysNode) PublishesToThirdParty() bool { return false }

type keyPair struct{ from, to string }

type keyRegex struct {
	re    *regexp.Regexp
	repl  string
	depth int
}

func (n *RenameKeysNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	pairs, err := parseKeyPairs(config)
	if err != nil {
		return nil, fmt.Errorf("data.rename_keys: %w", err)
	}
	regexes, err := parseKeyRegexes(config)
	if err != nil {
		return nil, fmt.Errorf("data.rename_keys: %w", err)
	}
	out := make([]workflow.Item, 0, len(input.Items))
	for _, item := range input.Items {
		j := deepCopyMap(item.JSON)
		for _, p := range pairs {
			v, ok := getPath(item.JSON, p.from, false)
			if !ok {
				continue
			}
			setPath(j, p.to, deepCopyValue(v), false)
			unsetPath(j, p.from, false)
		}
		for _, r := range regexes {
			renameKeysByRegex(j, r, r.depth)
		}
		out = append(out, workflow.Item{JSON: j, Binary: item.Binary})
	}
	return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
}

func parseKeyPairs(config map[string]interface{}) ([]keyPair, error) {
	list, err := cfgList(config, "keys")
	if err != nil {
		return nil, err
	}
	var pairs []keyPair
	for i, raw := range list {
		m, ok := raw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("keys[%d]: expected an object with current_key and new_key", i)
		}
		p := keyPair{
			from: cfgString(m, "current_key", cfgString(m, "currentKey", "")),
			to:   cfgString(m, "new_key", cfgString(m, "newKey", "")),
		}
		if p.from == "" || p.to == "" || p.from == p.to {
			continue
		}
		pairs = append(pairs, p)
	}
	return pairs, nil
}

func parseKeyRegexes(config map[string]interface{}) ([]keyRegex, error) {
	list, err := cfgList(config, "regex_replacements")
	if err != nil {
		return nil, err
	}
	var out []keyRegex
	for i, raw := range list {
		m, ok := raw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("regex_replacements[%d]: expected an object with search_regex and replace_regex", i)
		}
		pattern := cfgString(m, "search_regex", cfgString(m, "searchRegex", ""))
		if pattern == "" {
			continue
		}
		opts, _ := m["options"].(map[string]interface{})
		if opts == nil {
			opts = m
		}
		if cfgBool(opts, "case_insensitive", cfgBool(opts, "caseInsensitive", false)) {
			pattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("regex_replacements[%d]: search_regex: %w", i, err)
		}
		depth, err := cfgInt(opts, "depth", -1)
		if err != nil {
			return nil, fmt.Errorf("regex_replacements[%d]: %w", i, err)
		}
		repl := cfgString(m, "replace_regex", cfgString(m, "replaceRegex", ""))
		out = append(out, keyRegex{re: re, repl: jsToGoReplacement(repl), depth: depth})
	}
	return out, nil
}

// renameKeysByRegex renames matching keys of obj and, while depth allows,
// of nested objects (arrays are descended without renaming their indexes).
// The first match in a key is replaced, as a non-global JavaScript regex does.
func renameKeysByRegex(v interface{}, r keyRegex, depth int) {
	switch t := v.(type) {
	case []interface{}:
		if depth == 0 {
			return
		}
		for _, e := range t {
			renameKeysByRegex(e, r, depth-1)
		}
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if depth != 0 {
				renameKeysByRegex(t[k], r, depth-1)
			}
			loc := r.re.FindStringSubmatchIndex(k)
			if loc == nil {
				continue
			}
			var dst []byte
			dst = r.re.ExpandString(dst, r.repl, k, loc)
			newKey := k[:loc[0]] + string(dst) + k[loc[1]:]
			if newKey != k {
				t[newKey] = t[k]
				delete(t, k)
			}
		}
	}
}

var jsGroupRef = regexp.MustCompile(`\$(\d+)`)

// jsToGoReplacement turns JavaScript-style $1 references into Go's ${1} so a
// following letter or digit is not read as part of the group name.
func jsToGoReplacement(s string) string {
	return jsGroupRef.ReplaceAllString(s, "$${$1}")
}
