package workflow

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// destination is one place a node's config sends to, and the field it is in.
type destination struct{ field, value string }

// egressDestinations lists where a checked node type's resolved config
// sends: the values of its destination keys (a port alone is no host: only a
// key named port, or ending _port, with digits is skipped, so a decimal or
// hex IP in a host key is still checked), the hosts of its connection_string,
// and every string value that is a URL as a whole. Each is listed as written
// and normalised (sendDestinations).
func egressDestinations(keys [][]string, resolved map[string]interface{}) []destination {
	var out []destination
	add := func(field string, v interface{}) {
		for _, d := range sendDestinations(v) {
			out = append(out, destination{field, d})
		}
	}
	for _, group := range keys {
		for _, k := range group {
			if k == "credential_platform" { // names a vendor, not a host
				continue
			}
			v := destinationValue(resolved, k)
			if s := strings.TrimSpace(fmtScalar(v)); (k == "port" || strings.HasSuffix(k, "_port")) && s != "" && strings.Trim(s, "0123456789") == "" {
				continue
			}
			add(k, v)
		}
	}
	if cs, _ := resolved["connection_string"].(string); strings.TrimSpace(cs) != "" {
		for _, h := range dsnHosts(cs) {
			add("connection_string", h)
		}
	}
	walkConfigStrings(resolved, "", func(path, s string) {
		if path != "connection_string" && !strings.HasPrefix(path, "credential.") && urlValue.MatchString(strings.TrimSpace(s)) {
			add(path, strings.TrimSpace(s))
		}
	})
	return out
}

var urlValue = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)

func fmtScalar(v interface{}) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

// walkConfigStrings calls f with the dotted path and value of every string in v.
func walkConfigStrings(v interface{}, path string, f func(path, s string)) {
	switch t := v.(type) {
	case string:
		f(path, t)
	case map[string]interface{}:
		for k, x := range t {
			p := k
			if path != "" {
				p = path + "." + k
			}
			walkConfigStrings(x, p, f)
		}
	case []interface{}:
		for _, x := range t {
			walkConfigStrings(x, path, f)
		}
	}
}

// sendTexts appends every string value of v to out.
func sendTexts(v interface{}, out []string) []string {
	walkConfigStrings(v, "", func(_, s string) { out = append(out, s) })
	return out
}

// destinationValue is the value at key of config, where a dotted key is a
// path into a nested block ("embedding.base_url").
func destinationValue(config map[string]interface{}, key string) interface{} {
	if v, ok := config[key]; ok {
		return v
	}
	block, rest, nested := strings.Cut(key, ".")
	if !nested {
		return nil
	}
	m, _ := config[block].(map[string]interface{})
	return destinationValue(m, rest)
}

// sendDestinations lists what v names for a send, each as the raw text and in
// the normalised form of destinationsOf (lowercase, no userinfo or path, no
// trailing dot): a send must pass for both, so a parser that reads the address
// differently (a backslash before an @) cannot slip a host past the list. A
// list of bare hosts ("a:9092,b:9092") is taken one by one, and a value that
// still holds an expression is kept as it is: it names no host the grant has.
func sendDestinations(v interface{}) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case []interface{}:
		var out []string
		for _, x := range t {
			out = append(out, sendDestinations(x)...)
		}
		return out
	case []string:
		var out []string
		for _, x := range t {
			out = append(out, sendDestinations(x)...)
		}
		return out
	case string:
		if strings.Contains(t, "{{") {
			return []string{t}
		}
		raw := []string{t}
		if !strings.Contains(t, "://") {
			raw = strings.Split(t, ",")
		}
		var out []string
		for _, r := range raw {
			if r = strings.TrimSpace(r); r != "" {
				out = append(out, r)
			}
		}
		return append(out, destinationsOf(t)...)
	}
	return destinationsOf(v)
}

// dsnHosts extracts the hosts a connection string connects to: a URL's
// authority (a list of hosts, userinfo dropped) and its host-like query
// parameters, the host of a MySQL tcp(host:port) address, and the host keys of
// a key=value string (libpq host=, ADO server= / Data Source=). When nothing
// can be extracted the whole string is the destination, so it is refused
// rather than passed.
func dsnHosts(dsn string) []string {
	dsn = strings.TrimSpace(dsn)
	var hosts []string
	add := func(h string) {
		for _, p := range strings.Split(h, ",") {
			if p = strings.TrimSpace(p); p != "" {
				hosts = append(hosts, p)
			}
		}
	}
	switch {
	case strings.Contains(dsn, "://"):
		rest := dsn[strings.Index(dsn, "://")+3:]
		auth, query := rest, ""
		if i := strings.IndexAny(auth, "/?#"); i >= 0 {
			auth, query = auth[:i], auth[i:]
		}
		if i := strings.LastIndex(auth, "@"); i >= 0 {
			auth = auth[i+1:]
		}
		add(auth)
		if i := strings.Index(query, "?"); i >= 0 {
			q, _ := url.ParseQuery(query[i+1:])
			for k, vs := range q {
				switch strings.ToLower(k) {
				case "host", "hostaddr", "addr", "addrs", "server", "hosts", "sentinel", "sentinels":
					for _, v := range vs {
						add(v)
					}
				}
			}
		}
	case strings.Contains(dsn, "("):
		for rest := dsn; ; {
			i := strings.Index(rest, "(")
			j := strings.Index(rest, ")")
			if i < 0 || j < i {
				break
			}
			add(rest[i+1 : j])
			rest = rest[j+1:]
		}
	case strings.Contains(dsn, "="):
		sep := func(r rune) bool { return unicode.IsSpace(r) }
		if strings.Contains(dsn, ";") {
			sep = func(r rune) bool { return r == ';' }
		}
		for _, tok := range strings.FieldsFunc(dsn, sep) {
			k, v, _ := strings.Cut(tok, "=")
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "host", "hostaddr":
				add(strings.TrimSpace(v))
			case "server", "data source", "address", "addr", "network address":
				v = strings.TrimPrefix(strings.TrimSpace(v), "tcp:")
				add(strings.SplitN(v, ",", 2)[0])
			}
		}
	default: // user:pass@host:3306/db
		rest := dsn
		if i := strings.LastIndex(rest, "@"); i >= 0 {
			rest = rest[i+1:]
		}
		if i := strings.IndexAny(rest, "/?"); i >= 0 {
			rest = rest[:i]
		}
		add(rest)
	}
	if len(hosts) == 0 {
		return []string{dsn}
	}
	return hosts
}
