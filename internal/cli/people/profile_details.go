package people

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// profileEntryLines renders each entry of a stored experience/education
// array as one line: the given fields that are set, joined by " · ".
func profileEntryLines(raw json.RawMessage, fields ...string) []string {
	var entries []map[string]interface{}
	if len(raw) == 0 || json.Unmarshal(raw, &entries) != nil {
		return nil
	}
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		var parts []string
		for _, f := range fields {
			if v, _ := e[f].(string); v != "" {
				parts = append(parts, v)
			}
		}
		if len(parts) > 0 {
			lines = append(lines, truncateStr(strings.Join(parts, " · "), 80))
		}
	}
	return lines
}

// profileDetailLabels orders and labels the profile_details keys in the
// `people get` table; keys not listed follow, labelled by their name.
var profileDetailLabels = []struct{ key, label string }{
	{"profile_category", "Profile Category"},
	{"account_type", "Account Type"},
	{"verification_type", "Verification"},
	{"is_private", "Private"},
	{"pronouns", "Pronouns"},
	{"links", "Link"},
	{"threads_handle", "Threads"},
	{"contact", "Contact"},
	{"likes_count", "Likes"},
	{"friend_count", "Friends"},
	{"affiliates_count", "Affiliates"},
	{"connection_count", "Connections"},
	{"connection_degree", "Connection"},
	{"current_company", "Company"},
	{"join_date", "Joined"},
	{"birth_date", "Born"},
	{"language", "Language"},
	{"highlights", "Highlights"},
	{"pinned_post", "Pinned Post"},
	{"banner_url", "Banner"},
	{"platform_id", "Platform ID"},
}

// profileDetailRows renders a stored profile_details object as table rows:
// one row per link, lists joined, objects as their values.
func profileDetailRows(raw json.RawMessage) [][]string {
	var d map[string]interface{}
	if len(raw) == 0 || json.Unmarshal(raw, &d) != nil {
		return nil
	}
	var rows [][]string
	add := func(key, label string) {
		v, ok := d[key]
		if !ok {
			return
		}
		delete(d, key)
		if key == "links" {
			links, _ := v.([]interface{})
			for i, l := range links {
				m, _ := l.(map[string]interface{})
				u, _ := m["url"].(string)
				if t, _ := m["title"].(string); t != "" && t != u {
					u = t + " · " + u
				}
				if u != "" {
					rows = append(rows, []string{labelOnFirst(i, "Links"), truncateStr(u, 80)})
				}
			}
			return
		}
		if s := detailText(v); s != "" {
			rows = append(rows, []string{label, truncateStr(s, 80)})
		}
	}
	for _, l := range profileDetailLabels {
		add(l.key, l.label)
	}
	rest := make([]string, 0, len(d))
	for k := range d {
		rest = append(rest, k)
	}
	sort.Strings(rest)
	for _, k := range rest {
		add(k, k)
	}
	return rows
}

// detailText renders one profile_details value as text.
func detailText(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "yes"
		}
		return "no"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []interface{}:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			if s := detailText(e); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			if s := detailText(t[k]); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " · ")
	}
	return ""
}

func labelOnFirst(i int, label string) string {
	if i == 0 {
		return label
	}
	return ""
}
