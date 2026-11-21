package noderegistry

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apipkg"
	dbnodes "github.com/monoes/mono-agent/internal/nodes/db"
	"github.com/monoes/mono-agent/internal/workflow"
)

// hostKey matches a config key that names where a node's request goes.
var hostKey = regexp.MustCompile(`(?i)^(.*_)?(host|hostname|url|uri|endpoint|instance|server|domain|brokers|proxy|base|baseurl)$|(?i)(host|url|endpoint)$`)

// notDestinations are the host-like keys of registered node types that a
// saved connection's secret never goes to, and why ("<type> <key>").
var notDestinations = map[string]string{
	"ai.extract_page url":               "a public page it reads; it takes no connection",
	"ai.read_page url":                  "a public page it reads; it takes no connection",
	"applications.create url":           "the posting's address, recorded; nothing is sent there",
	"browser.jev url":                   "the page the user's browser opens; no connection's secret goes with it",
	"browser.read url":                  "the page the user's browser opens; no connection's secret goes with it",
	"comm.telegram photo_url":           "media Telegram fetches; the bot token goes only to api.telegram.org",
	"comm.twilio url":                   "TwiML Twilio fetches; the auth token goes only to api.twilio.com; public http(s) only (checkTwiMLURL), and the card shows it",
	"comm.whatsapp media_url":           "media WhatsApp fetches; the token goes only to graph.facebook.com",
	"core.form redirectUrl":             "where the form sends the person who filled it in; no connection",
	"data.convert_to_file url":          "the URL property of a calendar event it writes",
	"data.ical url":                     "the URL property of a calendar event it writes",
	"http.request pagination_next_url":  "a template over the response: a next page on another host goes without the request's auth (prepare)",
	"mcp.call server":                   "a vault entry's name; no saved connection, and a run the user hasn't approved resolves no vault entry",
	"mcp.call url":                      "no saved connection: its headers come from the vault entry, which a run the user hasn't approved doesn't resolve",
	"publication.register url":          "the publication's address, recorded",
	"publication.register parent_url":   "the parent's address, recorded",
	"service.airtable base":             "a base id in api.airtable.com's path",
	"service.bluesky uri":               "an at:// record; the session goes only to bsky.social",
	"service.bluesky post_uri":          "an at:// record; the session goes only to bsky.social",
	"service.devto canonical_url":       "article metadata; the key goes only to dev.to",
	"service.discord webhook_url":       "the webhook URL is its own secret: no bot token or Authorization goes with it",
	"service.discord avatar_url":        "the webhook's avatar, which Discord fetches",
	"service.github base":               "a pull request's base branch",
	"service.hashnode publication_host": "the blog's domain, a GraphQL argument; the token goes only to gql.hashnode.com",
	"service.hubspot domain":            "a company's domain property; the token goes only to api.hubapi.com",
	"service.reddit url":                "the link a post submits; the token goes only to oauth.reddit.com",
	"service.stripe success_url":        "where Stripe sends the payer; the key goes only to api.stripe.com",
	"service.stripe cancel_url":         "where Stripe sends the payer; the key goes only to api.stripe.com",
	"system.rss_read url":               "a public feed; it takes no connection",
	"vault.secret_save url":             "stored with the secret; nothing is sent there",
}

// notDestinationReads are the host-like config reads in a node package's
// source that are no destination of a connection's secret, and why
// ("<package dir> <key>"). Reads a declared key of the package's types
// covers need no entry.
var notDestinationReads = map[string]string{
	"agent base_url":           "read from the vendor connection's own data (config[\"credential\"]), which a step can't set",
	"agent host":               "read from the vendor connection's own data (config[\"credential\"]), which a step can't set",
	"ai/crawl url":             "a public page it reads; it takes no connection",
	"applications url":         "the posting's address, recorded",
	"browserjev url":           "the page the user's browser opens",
	"comm media_url":           "media WhatsApp fetches; the token goes only to graph.facebook.com",
	"comm photo_url":           "media Telegram fetches; the bot token goes only to api.telegram.org",
	"comm url":                 "Twilio's TwiML URL: public http(s) only (checkTwiMLURL), shown on the card",
	"data url":                 "the URL property of a calendar event it writes",
	"db vhost":                 "a virtual host on the broker the connection names, not a host",
	"http pagination_next_url": "a next page on another host goes without the request's auth (prepare)",
	"mcp server":               "a vault entry's name; no saved connection",
	"mcp url":                  "no saved connection: its headers come from the vault entry, which a run the user hasn't approved doesn't resolve",
	"service avatar_url":       "a Discord webhook's avatar",
	"service webhook_url":      "a Discord webhook URL, its own secret: no token goes with it",
	"service base":             "a pull request's base branch",
	"service cancel_url":       "where Stripe sends the payer",
	"service success_url":      "where Stripe sends the payer",
	"service canonical_url":    "article metadata",
	"service post_uri":         "an at:// record",
	"service uri":              "an at:// record",
	"service publication_host": "a GraphQL argument; the token goes only to gql.hashnode.com",
	"service url":              "the link a Reddit post submits",
	"system url":               "a public feed",
	"vault url":                "stored with the secret",
}

// declaredKeys flattens an executor's destination groups ("embedding.url"
// counts as url).
func declaredKeys(groups [][]string) map[string]bool {
	out := map[string]bool{}
	for _, g := range groups {
		for _, k := range g {
			if _, after, ok := strings.Cut(k, "."); ok {
				k = after
			}
			out[k] = true
		}
	}
	return out
}

// Every registered node type keeps a saved connection's secret on the
// hosts the connection, or the user's workflow as written, names
// (workflow.CheckCredentialDestinations): each host-like field its schema
// has is one of its destination keys, or a field the secret never goes to
// with the reason. An API package's types send it only to hosts the package
// names (apipkg's opRun.authHost). A node added with a host field and
// neither fails here.
func TestEveryNodeTypesHostFieldsKeepTheConnectionsSecret(t *testing.T) {
	registry := Build(nil)
	seen := map[string]bool{}
	for _, nodeType := range registry.Types() {
		if isSocialBrowserType(nodeType) {
			continue
		}
		factory, _ := registry.Get(nodeType)
		exec := factory()
		if _, ok := exec.(*apipkg.Node); ok {
			continue
		}
		var declared map[string]bool
		if d, ok := exec.(workflow.CredentialDestinations); ok {
			declared = declaredKeys(d.CredentialDestinationKeys())
		}
		data, ok := workflow.ReadEmbeddedSchema(nodeType)
		if !ok {
			continue
		}
		var schema workflow.NodeSchema
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Fatalf("%s: %v", nodeType, err)
		}
		for _, f := range schema.Fields {
			if !hostKey.MatchString(f.Key) || declared[f.Key] {
				continue
			}
			k := nodeType + " " + f.Key
			seen[k] = true
			if notDestinations[k] == "" {
				t.Errorf("%s: field %q names a host, but it is neither one of the type's CredentialDestinationKeys nor in notDestinations with a reason", nodeType, f.Key)
			}
		}
	}
	for k := range notDestinations {
		if !seen[k] {
			t.Errorf("notDestinations lists %q, which no registered type's schema has (or the type declares it now)", k)
		}
	}
}

// The same over the node packages' source: every host-like key a node
// reads from its config is a destination key of a type in its package, or
// in notDestinationReads with the reason. A read a schema doesn't list
// (Jira's base_url, the queues' hostname) is caught here.
func TestEveryHostLikeConfigReadIsADestinationKey(t *testing.T) {
	registry := Build(nil)
	declared := map[string]map[string]bool{} // package dir under internal/nodes → keys
	add := func(dir string, groups [][]string) {
		if declared[dir] == nil {
			declared[dir] = map[string]bool{}
		}
		for k := range declaredKeys(groups) {
			declared[dir][k] = true
		}
	}
	const prefix = "github.com/monoes/mono-agent/internal/nodes/"
	for _, nodeType := range registry.Types() {
		factory, _ := registry.Get(nodeType)
		exec := factory()
		d, ok := exec.(workflow.CredentialDestinations)
		if !ok {
			continue
		}
		typ := reflect.TypeOf(exec)
		for typ.Kind() == reflect.Ptr {
			typ = typ.Elem()
		}
		if dir, ok := strings.CutPrefix(typ.PkgPath(), prefix); ok {
			add(dir, d.CredentialDestinationKeys())
		}
	}
	for nodeType := range dbnodes.TriggerSubscribers {
		add("db", dbnodes.TriggerDestinationKeys(nodeType))
	}

	reads := hostLikeReads(t, filepath.Join("..", "nodes"))
	seen := map[string]bool{}
	for _, r := range reads {
		if declared[r.dir][r.key] {
			continue
		}
		byFile := r.dir + " " + r.key + "@" + r.file
		byKey := r.dir + " " + r.key
		switch {
		case notDestinationReads[byFile] != "":
			seen[byFile] = true
		case notDestinationReads[byKey] != "":
			seen[byKey] = true
		default:
			t.Errorf("internal/nodes/%s/%s reads %q from its config, a host-like key that is neither a CredentialDestinationKeys key of a type in its package nor in notDestinationReads with a reason", r.dir, r.file, r.key)
		}
	}
	for k := range notDestinationReads {
		if !seen[k] {
			t.Errorf("notDestinationReads lists %q, which no node package reads (or its package declares it now)", k)
		}
	}
}

type configRead struct{ dir, file, key string }

// configName is what the node packages call a config map.
var configName = regexp.MustCompile(`^(config|cfg|itemConfig|conf|c|params|opts)$`)

func isConfigExpr(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return configName.MatchString(x.Name)
	case *ast.SelectorExpr:
		return x.Sel.Name == "cfg" || x.Sel.Name == "config" || x.Sel.Name == "Config"
	}
	return false
}

// hostLikeReads finds, in the non-test Go source under root, every
// host-like string literal read from a config map: config["k"], or a call
// that takes the config and the key (strVal(config, "k"), strOr(cfg, def,
// "k1", "k2")).
func hostLikeReads(t *testing.T, root string) []configRead {
	t.Helper()
	found := map[configRead]bool{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			return err
		}
		dir, _ := filepath.Rel(root, filepath.Dir(p))
		note := func(e ast.Expr) {
			lit, ok := e.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return
			}
			if s, err := strconv.Unquote(lit.Value); err == nil && hostKey.MatchString(s) {
				found[configRead{filepath.ToSlash(dir), filepath.Base(p), s}] = true
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.IndexExpr:
				if isConfigExpr(x.X) {
					note(x.Index)
				}
			case *ast.CallExpr:
				takesConfig := false
				for _, a := range x.Args {
					takesConfig = takesConfig || isConfigExpr(a)
				}
				if takesConfig {
					for _, a := range x.Args {
						note(a)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]configRead, 0, len(found))
	for r := range found {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].dir+out[i].file+out[i].key < out[j].dir+out[j].file+out[j].key
	})
	if len(out) == 0 {
		t.Fatal("found no config reads under " + root)
	}
	return out
}
