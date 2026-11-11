package noderegistry

import (
	"sort"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apipkg"
	"github.com/monoes/mono-agent/internal/hilsend"
)

// Issue #1418: a node that publishes to third parties is refused, under
// agent access, when its run has read a granted secret. Which nodes those are
// is registry-derived: a node type is publishing unless it says it is
// local-only (fail closed), and a type that is in neither list below fails
// this test, so a new node must be classified when it is added.
//
// A node declares local-only with
//
//	PublishesToThirdParty() bool   // false
//
// on its executor. This file is the reviewed classification; the engine
// guard (the next slice) reads the declaration, and the pin below ties the
// two together.

// localOnlyTypes send nothing off this machine, and why.
var localOnlyTypes = map[string]string{
	"agent.memory":            "a sub-node's configuration; it does no I/O of its own",
	"ai.memory":               "a sub-node's configuration; it does no I/O of its own",
	"ai.model":                "a sub-node's configuration; it does no I/O of its own",
	"ai.output_parser":        "a sub-node's configuration; it does no I/O of its own",
	"ai.task":                 "queues a coder task locally, never starts or waits",
	"applications.create":     "local records",
	"applications.evaluate":   "local scoring",
	"applications.list":       "local records",
	"applications.prepare":    "local drafting",
	"applications.set_status": "local records",
	"applications.tag":        "local records",
	"core.aggregate":          "regroups items",
	"core.code":               "runs its own script; no outbound call of its own (an unapproved run may not run code, code_unapproved_test)",
	"core.compare_datasets":   "compares items",
	"core.data_table":         "the local data tables",
	"core.evaluation":         "scores items locally",
	"core.execute_workflow":   "runs a sub-workflow; the taint is seeded into it (SeedResolved)",
	"core.filter":             "filters items",
	"core.form":               "serves a form on this machine; items are not sent anywhere",
	"core.human_in_loop":      "shows the owner a card (the card itself is a persistence question, hilsend)",
	"core.if":                 "routes items",
	"core.limit":              "trims items",
	"core.merge":              "joins items",
	"core.noop":               "passes items",
	"core.remove_duplicates":  "local history",
	"core.set":                "edits items",
	"core.sort":               "orders items",
	"core.split_in_batches":   "batches items",
	"core.stop_error":         "ends the run",
	"core.switch":             "routes items",
	"core.wait":               "pauses",
	"data.compression":        "local data/file/image processing; no outbound call",
	"data.convert_to_file":    "local data/file/image processing; no outbound call",
	"data.crypto":             "local data/file/image processing; no outbound call",
	"data.datetime":           "local data/file/image processing; no outbound call",
	"data.debug_helper":       "local data/file/image processing; no outbound call",
	"data.extract_from_file":  "local data/file/image processing; no outbound call",
	"data.html":               "local data/file/image processing; no outbound call",
	"data.ical":               "local data/file/image processing; no outbound call",
	"data.jwt":                "local data/file/image processing; no outbound call",
	"data.markdown":           "local data/file/image processing; no outbound call",
	"data.read_write_file":    "local data/file/image processing; no outbound call",
	"data.rename_keys":        "local data/file/image processing; no outbound call",
	"data.split_out":          "local data/file/image processing; no outbound call",
	"data.spreadsheet":        "local data/file/image processing; no outbound call",
	"data.summarize":          "local data/file/image processing; no outbound call",
	"data.totp":               "local data/file/image processing; no outbound call",
	"data.write_binary_file":  "local data/file/image processing; no outbound call",
	"data.xml":                "local data/file/image processing; no outbound call",
	"documents.render":        "renders a file locally",
	"image.adjust":            "local data/file/image processing; no outbound call",
	"image.convert":           "local data/file/image processing; no outbound call",
	"image.crop":              "local data/file/image processing; no outbound call",
	"image.edit":              "local data/file/image processing; no outbound call",
	"image.info":              "local data/file/image processing; no outbound call",
	"image.remove_background": "local data/file/image processing; no outbound call",
	"image.resize":            "local data/file/image processing; no outbound call",
	"image.thumbnail":         "local data/file/image processing; no outbound call",
	"image.vault_get":         "local data/file/image processing; no outbound call",
	"image.vault_save":        "local data/file/image processing; no outbound call",
	"people.save":             "local records",
	"publication.register":    "records an address; nothing is sent there",
	"vault.secret_get":        "reads the local vault into the run (the taint)",
	"vault.secret_save":       "writes the local vault",
	"vector.embeddings":       "a sub-node's configuration; it does no I/O of its own (the root node embeds)",
	"vector.document_loader":  "loads a local file",
	"vector.load_document":    "loads a local file",
	"vector.split_text":       "splits text",
	"vector.text_splitter":    "splits text",
}

// publishingTypes send data off this machine (a message, a request, a
// record, a response, a prompt), and what carries it. Every type of the
// "app." family (the API packages) is publishing by rule.
var publishingTypes = map[string]string{
	"agent.ask":                   "model provider (monomind runner)",
	"ai.agent":                    "model provider / the page it fetches",
	"ai.chat":                     "model provider / the page it fetches",
	"ai.choose":                   "model provider / the page it fetches",
	"ai.classify":                 "model provider / the page it fetches",
	"ai.embed":                    "model provider / the page it fetches",
	"ai.extract":                  "model provider / the page it fetches",
	"ai.extract_page":             "model provider / the page it fetches",
	"ai.guardrails":               "model provider / the page it fetches",
	"ai.read_page":                "model provider / the page it fetches",
	"ai.transform":                "model provider / the page it fetches",
	"browser.jev":                 "types into a page in the user's browser",
	"comm.email_read":             "a message or mailbox request to a third party",
	"comm.email_send":             "a message or mailbox request to a third party",
	"comm.slack":                  "a message or mailbox request to a third party",
	"comm.telegram":               "a message or mailbox request to a third party",
	"comm.twilio":                 "a message or mailbox request to a third party",
	"comm.whatsapp":               "a message or mailbox request to a third party",
	"core.respond_chat":           "the response goes to whoever called the trigger",
	"core.respond_webhook":        "the response goes to whoever called the trigger",
	"db.cratedb":                  "a remote database server named by the config",
	"db.ldap":                     "a remote database server named by the config",
	"db.mongodb":                  "a remote database server named by the config",
	"db.mssql":                    "a remote database server named by the config",
	"db.mysql":                    "a remote database server named by the config",
	"db.oracle":                   "a remote database server named by the config",
	"db.postgres":                 "a remote database server named by the config",
	"db.questdb":                  "a remote database server named by the config",
	"db.redis":                    "a remote database server named by the config",
	"db.snowflake":                "a remote database server named by the config",
	"db.timescaledb":              "a remote database server named by the config",
	"discovery.search_jobs":       "a job source's query",
	"http.ftp":                    "a request to a host the config names",
	"http.graphql":                "a request to a host the config names",
	"http.request":                "a request to a host the config names",
	"http.ssh":                    "a request to a host the config names",
	"mcp.call":                    "an MCP server",
	"org.ask":                     "an org's messages, which leave through it",
	"org.run":                     "an org's messages, which leave through it",
	"org.send":                    "an org's messages, which leave through it",
	"people.sync_outlook_message": "the Microsoft Graph request",
	"queue.amqp":                  "a broker named by the config",
	"queue.kafka":                 "a broker named by the config",
	"queue.mqtt":                  "a broker named by the config",
	"queue.rabbitmq":              "a broker named by the config",
	"service.airtable":            "a vendor API",
	"service.asana":               "a vendor API",
	"service.bluesky":             "a vendor API",
	"service.devto":               "a vendor API",
	"service.discord":             "a vendor API",
	"service.github":              "a vendor API",
	"service.gmail":               "a vendor API",
	"service.google_drive":        "a vendor API",
	"service.google_sheets":       "a vendor API",
	"service.hashnode":            "a vendor API",
	"service.hubspot":             "a vendor API",
	"service.huggingface":         "a vendor API",
	"service.jira":                "a vendor API",
	"service.linear":              "a vendor API",
	"service.mastodon":            "a vendor API",
	"service.notion":              "a vendor API",
	"service.openrouter":          "a vendor API",
	"service.outlook_mail":        "a vendor API",
	"service.producthunt":         "a vendor API",
	"service.reddit":              "a vendor API",
	"service.salesforce":          "a vendor API",
	"service.shopify":             "a vendor API",
	"service.stripe":              "a vendor API",
	"service.youtube":             "a vendor API",
	"system.execute_command":      "runs a command, which can reach anywhere",
	"system.rss_read":             "the feed it fetches",
	"vector.embed":                "an embedding provider or a vector store",
	"vector.store":                "an embedding provider or a vector store",
}

// publishingByPrefix are families whose every type publishes: the API
// packages, and the social platforms' browser types (registered from
// automations, not by Build).
var publishingByPrefix = []string{"app.", "instagram.", "linkedin.", "tiktok.", "x.", "hackernews.", "producthunt."}

func classifiedPublishing(nodeType string) (publishing, ok bool) {
	if _, l := localOnlyTypes[nodeType]; l {
		return false, true
	}
	if _, p := publishingTypes[nodeType]; p {
		return true, true
	}
	for _, p := range publishingByPrefix {
		if strings.HasPrefix(nodeType, p) {
			return true, true
		}
	}
	return false, false
}

// declaresPublishing is what an executor says about itself.
type declaresPublishing interface{ PublishesToThirdParty() bool }

// Every registered node type is classified, and every classification names
// a registered type.
func TestEveryNodeTypeIsClassifiedAsPublishingOrLocalOnly(t *testing.T) {
	registry := Build(nil)
	registered := map[string]bool{}
	var unclassified []string
	for _, nt := range registry.Types() {
		registered[nt] = true
		if _, ok := classifiedPublishing(nt); !ok {
			unclassified = append(unclassified, nt)
		}
	}
	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		t.Errorf("node types in neither localOnlyTypes nor publishingTypes (a type with no declaration is publishing; say which it is): %v", unclassified)
	}
	for _, m := range []map[string]string{localOnlyTypes, publishingTypes} {
		for nt := range m {
			if !registered[nt] {
				t.Errorf("%s is classified but not registered: remove it from the table", nt)
			}
		}
	}
	for nt := range localOnlyTypes {
		if _, dup := publishingTypes[nt]; dup {
			t.Errorf("%s is in both tables", nt)
		}
	}
}

// Every type the send gate knows is publishing, so the list cannot lag it.
func TestEverySendGateTypeIsPublishing(t *testing.T) {
	registry := Build(nil)
	check := func(nt, why string) {
		if pub, ok := classifiedPublishing(nt); ok && !pub {
			t.Errorf("%s is %s but classified local-only", nt, why)
		}
	}
	for _, s := range hilsend.NativeSends() {
		check(s.NodeType, "a native send of the HIL gate")
	}
	for _, nt := range registry.Types() {
		if apipkg.SendsSome(nt) {
			check(nt, "an API package type with an annotated send")
		}
	}
}

// The engine reads the declaration, not this table: a local-only type must
// declare PublishesToThirdParty() == false, and a publishing type must not.
// Red until the declarations exist.
func TestExecutorsDeclareWhatTheTableClassifies(t *testing.T) {
	registry := Build(nil)
	var missing, wrong []string
	for _, nt := range registry.Types() {
		pub, ok := classifiedPublishing(nt)
		if !ok {
			continue
		}
		factory, _ := registry.Get(nt)
		d, declared := factory().(declaresPublishing)
		switch {
		case !pub && !declared:
			missing = append(missing, nt)
		case declared && d.PublishesToThirdParty() != pub:
			wrong = append(wrong, nt)
		}
	}
	sort.Strings(missing)
	sort.Strings(wrong)
	if len(missing) > 0 {
		t.Errorf("%d local-only type(s) do not declare PublishesToThirdParty() false, so the engine would treat them as publishing: %v", len(missing), missing)
	}
	if len(wrong) > 0 {
		t.Errorf("type(s) whose declaration disagrees with the table: %v", wrong)
	}
}
