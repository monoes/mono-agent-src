package n8nimport

import (
	"fmt"
	"strings"
	"time"

	dbnodes "github.com/monoes/mono-agent/internal/nodes/db"
)

// Message-queue and database triggers become mono's streaming triggers
// (trigger.kafka, trigger.postgres, …), whose consumers and n8n parameter
// tables live in internal/nodes/db. The Local File Trigger becomes a poll
// that lists the watched files and passes on those not seen before.

// queueTriggerCredentials maps each n8n queue/database trigger to the mono
// connection platform and the n8n credential type it stands for.
var queueTriggerCredentials = map[string][2]string{
	"n8n-nodes-base.kafkaTrigger":    {"kafka", "kafka"},
	"n8n-nodes-base.rabbitmqTrigger": {"rabbitmq", "rabbitmq"},
	"n8n-nodes-base.amqpTrigger":     {"amqp", "amqp"},
	"n8n-nodes-base.mqttTrigger":     {"mqtt", "mqtt"},
	"n8n-nodes-base.redisTrigger":    {"redis", "redis"},
	"n8n-nodes-base.postgresTrigger": {"postgresql", "postgres"},
}

func init() {
	for t := range queueTriggerCredentials {
		Register(t, mapQueueTrigger)
	}
	Register("n8n-nodes-base.localFileTrigger", mapLocalFileTrigger)
}

func mapQueueTrigger(c *MapContext) Mapping {
	spec := dbnodes.N8nNodeMappings[c.Type]
	// n8n names a binding's routing key routingKey; the consumer reads routing_key.
	lists := map[string]map[string]string{"bindings": {"routingKey": "routing_key"}}
	m := mapWithParams(c, paramSpec{Type: spec.MonoType, Params: spec.Params, ListFields: lists})
	for k, v := range spec.Defaults {
		if _, set := m.Config[k]; !set {
			m.Config[k] = v
		}
	}
	cred := queueTriggerCredentials[c.Type]
	m.Config["credential_id"] = c.Credential(cred[0], cred[1])
	return m
}

// localFileScript prints "<mtime>\t<path>" for every file under $1 (or
// for $1 itself), with GNU or BSD stat.
const localFileScript = `find %s"$1"%s -type f -exec sh -c 'for f; do printf "%%s\t%%s\n" "$(stat -c %%Y "$f" 2>/dev/null || stat -f %%m "$f")" "$f"; done' sh {} +`

// mapLocalFileTrigger polls the file or folder every minute: a shell step
// lists the files with their modification time, a code step turns the
// lines into {path, event} items, and core.remove_duplicates passes on
// only files (or file versions) no earlier poll saw.
func mapLocalFileTrigger(c *MapContext) Mapping {
	on := c.Literal("triggerOn", "folder")
	path := c.Str("path", "")
	follow, depth := "", ""
	if c.Bool("options.followSymlinks", true) {
		follow = "-L "
	}
	event := "change"
	if on == "folder" {
		if d := int(c.Float("options.depth", -1)); d >= 0 {
			depth = fmt.Sprintf(" -maxdepth %d", d+1)
		}
		var events []string
		for _, e := range toList(c.Raw("events")) {
			events = append(events, fmt.Sprint(e))
		}
		var dropped []string
		watched := map[string]bool{}
		for _, e := range events {
			if e == "add" || e == "change" {
				watched[e] = true
			} else {
				dropped = append(dropped, e)
			}
		}
		if len(dropped) > 0 {
			c.Warn("events", events, "only added and changed files can be seen by a poll; %s is not reported", strings.Join(dropped, ", "))
		}
		if watched["add"] && !watched["change"] {
			event = "add"
		} else if watched["add"] {
			c.Note("new and changed files both pass with event \"change\"")
		}
	} else {
		depth = " -maxdepth 0"
		c.Ignore("events")
	}
	c.Ignore("options.depth", "options.followSymlinks", "options.usePolling", "options.awaitWriteFinish")
	if c.Has("options.ignored") {
		c.Unsupported("options.ignored")
		c.Ignore("options.ignoreMode")
	}
	since := int64(0)
	if c.Bool("options.ignoreInitial", true) {
		since = time.Now().Unix()
	}
	list := map[string]interface{}{
		"command": "sh",
		"args":    []interface{}{"-c", fmt.Sprintf(localFileScript, follow, depth), "sh", path},
	}
	parse := fmt.Sprintf(`var event = %q, since = %d;
var out = [];
items.forEach(function (it) {
  String(it.json.stdout || "").split("\n").forEach(function (line) {
    var tab = line.indexOf("\t");
    if (tab < 0) { return; }
    var mtime = Number(line.slice(0, tab));
    if (mtime < since) { return; }
    out.push({ path: line.slice(tab + 1), event: event, mtime: mtime });
  });
});
return out;
`, event, since)
	dedupe := "={{ $json.path }}|{{ $json.mtime }}"
	if event == "add" {
		dedupe = "={{ $json.path }}"
	}
	c.Note("polls %s every minute through sh (find, stat) and passes on files no earlier poll saw", path)
	if since > 0 {
		c.Note("files last modified before the import are skipped, as n8n ignored files present when it started watching")
	}
	m := Mapping{Type: "trigger.schedule", Config: map[string]interface{}{"cron": "0 * * * * *"}, EntrySuffix: " (poll)"}
	m.Chain = []ChainStep{
		{Type: "system.execute_command", Suffix: " (list)", Config: list},
		{Type: "core.code", Suffix: " (files)", Config: codeConfig(wrapCode(parse))},
		{Type: "core.remove_duplicates", Config: map[string]interface{}{
			"operation": "removeItemsRepeatedWithinPreviousExecutions", "dedupeValue": c.Translate("dedupe", dedupe)}},
	}
	return m
}

// toList returns v as a list (a single value becomes a one-element list).
func toList(v interface{}) []interface{} {
	switch x := v.(type) {
	case nil:
		return nil
	case []interface{}:
		return x
	default:
		return []interface{}{x}
	}
}
