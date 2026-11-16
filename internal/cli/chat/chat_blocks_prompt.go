package chat

// chatBlocksPrompt teaches a model the declarative chat-block convention the
// GUI chat renders (docs/mastermind/specs/2026-11-16-chat-blocks.md). It is
// the single source for every runtime that talks to the GUI chat.
func chatBlocksPrompt() string {
	return "The chat can render a visual block: a fenced code block whose info string is the block type and whose body is ONE valid JSON object. " +
		"Use a block only when it beats prose; for simple answers write plain prose. Types:\n" +
		"- choices: ask the user to pick. ```choices {\"question\":\"Which?\",\"options\":[{\"id\":\"a\",\"label\":\"main\"},{\"id\":\"b\",\"label\":\"dev\"}]}``` (2-12 options, optional \"multi\":true)\n" +
		"- chart: {\"type\":\"bar\"|\"line\"|\"area\",\"x\":[\"Mon\",\"Tue\"],\"series\":[{\"name\":\"runs\",\"data\":[4,6]}]}; or {\"type\":\"donut\",\"slices\":[{\"label\":\"ok\",\"value\":9}]}, {\"type\":\"sparkline\",\"data\":[1,3,2]}, {\"type\":\"stat\",\"stats\":[{\"label\":\"Runs\",\"value\":12}]}\n" +
		"- table: {\"columns\":[{\"key\":\"name\",\"label\":\"Name\"}],\"rows\":[{\"name\":\"A\",\"link\":\"monoagent://workflow/ID\"}]} (optional per-row \"link\" goes on the row object, never as a column; max 8 columns, 100 rows)\n" +
		"- card: {\"kind\":\"workflow\",\"title\":\"Daily sync\",\"status\":\"ok\",\"tone\":\"ok\",\"link\":\"monoagent://workflow/ID\"}; list: {\"items\":[<card>,...]}\n" +
		"- steps: {\"steps\":[{\"label\":\"Fetch\",\"state\":\"done\"}]} (state: todo|active|done|failed)\n" +
		"- form: {\"fields\":[{\"id\":\"n\",\"type\":\"text\"|\"number\"|\"select\"|\"checkbox\",\"label\":\"Name\"}]} (select needs \"options\":[{\"value\",\"label\"}])\n" +
		"Rules: valid JSON only; each string at most 200 characters; keep data small; never put secrets or credentials in a block; " +
		"use a monoagent://kind/id link only for an entity you know exists (taken from a tool result), never invent ids; no images, HTML or other URLs."
}
