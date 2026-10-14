package catalog

// Node reference entries (`monoagentcli ref node <type>`), one variable per
// category; catalog.go joins them into NodeDocs.

// nodeDocsAi is the "ai" category of the node catalogue.
var nodeDocsAi = []nodeDoc{
	{
		Type:     "ai.agent",
		Category: "ai",
		Short:    "DEPRECATED — fails at run time; use agent.ask",
		Description: `Kept only so old workflows get a migration hint instead of "unknown node type".
Running it fails with: replace it with the "agent.ask" node.`,
	},
	{
		Type:     "ai.chat",
		Category: "ai",
		Short:    "DEPRECATED — fails at run time; use agent.ask",
		Description: `Kept only so old workflows get a migration hint instead of "unknown node type".
Running it fails with: replace it with the "agent.ask" node (local AI agent via monomind; see "monoagentcli ref node agent.ask").`,
	},
	{
		Type:     "ai.classify",
		Category: "ai",
		Short:    "DEPRECATED — fails at run time; use ai.choose (TypeSafe Jev) or agent.ask",
		Description: `Kept only so old workflows get a migration hint instead of "unknown node type".
Running it fails with: replace it with the "ai.choose" node (TypeSafe Jev; see "monoagentcli ref node ai.choose") or an "agent.ask" node whose prompt requests classification.`,
	},
	{
		Type:     "ai.extract",
		Category: "ai",
		Short:    "DEPRECATED — fails at run time; use agent.ask with a JSON-extraction prompt",
		Description: `Kept only so old workflows get a migration hint instead of "unknown node type".
Running it fails with: replace it with the "agent.ask" node whose prompt requests JSON extraction.`,
	},
	{
		Type:     "ai.transform",
		Category: "ai",
		Short:    "DEPRECATED — fails at run time; use agent.ask",
		Description: `Kept only so old workflows get a migration hint instead of "unknown node type".
Running it fails with: replace it with the "agent.ask" node.`,
	},
	{
		Type:     "ai.read_page",
		Category: "ai",
		Short:    "Fetch a URL and return its main content as clean markdown (no AI)",
		Config:   `{ "url": "{{ $json.link }}", "include_links": true, "max_tokens": 4000 }`,
		Inputs:   "item with URL (config url wins over the item's url)",
		Outputs:  "url, title, description, author, published_at, markdown, main_text, headings, token_count",
	},
	{
		Type:     "ai.extract_page",
		Category: "ai",
		Short:    "Fetch a URL and extract fields with CSS selectors you give or a local agent writes",
		Description: `extract_mode "natural" (the default) sends the cleaned page and your prompt to a
local agent runtime through monomind, which writes the selectors; the runtime is
MONOAGENT_AI_RUNTIME, else the first installed one (claude first). No API key is
involved; if the agent fails, "extracted" holds the page markdown and "error" says
why. extract_mode "css" uses the selectors in "fields" ("selector" or
"selector@attr") and needs no AI. Set "list_selector" to extract one row per
matching element.`,
		Config: `{ "url": "{{ $json.link }}", "extract_mode": "natural", "prompt": "the headline and publication date" }
// or, without AI:
{ "url": "{{ $json.link }}", "extract_mode": "css", "fields": { "headline": "h1", "date": "time@datetime" } }`,
		Inputs:  "item with URL (config url wins over the item's url)",
		Outputs: "url, extracted (object, or a list with list_selector), selectors_used, extract_mode, fetch_time_ms",
	},
	{
		Type:     "ai.embed",
		Category: "ai",
		Short:    "DEPRECATED — fails at run time; no local-agent equivalent",
		Description: `Kept only so old workflows get a migration hint instead of "unknown node type".
Running it fails with: no local-agent equivalent exists — remove the node, or approximate via an "agent.ask" prompt.`,
	},
}

// nodeDocsGemini is the "gemini" category of the node catalogue.
var nodeDocsGemini = []nodeDoc{
	{
		Type:     "gemini.generate_text",
		Category: "gemini",
		Short:    "Send a prompt to Gemini and return generated text",
		Description: `NO API KEY REQUIRED. Uses browser automation — monoagentcli opens gemini.google.com
in a real Chrome session, types the prompt, and reads the response back.

Setup (one-time): run  monoagentcli login gemini  and log in with your Google account.
After that, credential_id is optional — if you have only one Gemini session it
is resolved automatically. Omit it entirely and monoagentcli will find it.`,
		Config: `{
  "prompt":         "{{ $json.my_prompt }}",
  "maxWaitSeconds": 90
}

// credential_id is OPTIONAL. Only needed if you have multiple Gemini sessions:
// "credential_id": "social:gemini:gemini-user"`,
		Inputs:  "item with prompt field (or hardcoded prompt)",
		Outputs: "response_text",
		Notes:   "Keep prompts concise. Very long prompts may trigger Gemini's reasoning mode which does not return plain text.",
	},
	{
		Type:     "gemini.chat_session",
		Category: "gemini",
		Short:    "Send a prompt to a persistent Gemini session (text or image)",
		Description: `NO API KEY REQUIRED. Uses browser automation with session persistence.

Unlike generate_text/generate_image which always start a fresh chat, chat_session
lets you continue an existing conversation. Pass the session_id returned from a
previous run to resume — Gemini will have full memory of the prior exchange.

Supports both text and image generation in the same node via the mode parameter.

Setup (one-time): run  monoagentcli login gemini  and log in with your Google account.`,
		Config: `{
  "prompt":     "Tell me more about goroutines",
  "session_id": "5c0b84d75812b533",
  "mode":       "text"
}

// session_id is OPTIONAL. Omit to start a new chat; the returned session_id
// can be passed to the next run to continue the same conversation.

// mode: "text" (default) or "image" — controls which response type to wait for.

// referenceImagePath is OPTIONAL — upload a local image before the prompt.

// Full options:
// {
//   "prompt":             "Your message",
//   "session_id":         "<id from previous run>",
//   "mode":               "text",
//   "referenceImagePath": "/path/to/image.jpg",
//   "maxWaitSeconds":     90,
//   "downloadDir":        "~/.monoagent/downloads"
// }`,
		Inputs:  "item with prompt (plus optional session_id, mode, referenceImagePath)",
		Outputs: "session_id, response_text (text mode) | session_id, images, image_count (image mode)",
		Notes:   "Save the session_id from each run and pass it back as input to maintain conversation context across multiple CLI calls or workflow executions.",
	},
	{
		Type:     "gemini.chat_session_many",
		Category: "gemini",
		Short:    "Send a LIST of prompts to one Gemini session, in order",
		Description: `NO API KEY REQUIRED. Same as chat_session, but takes an array of prompts and
sends them one after another inside a single chat, so every answer/image shares
the same context — characters, style and look stay consistent across the batch.

The number of prompts is not fixed: the node loops over however many the array
holds. Prefer this over chaining N chat_session nodes.

In image mode each generated image is downloaded and registered in the image
vault as it is produced.

Setup (one-time): run  monoagentcli login gemini  and log in with your Google account.`,
		Config: `{
  "prompts": ["a wizard in a blue robe", "the same wizard riding a red dragon"],
  "mode":    "image"
}

// When the array comes from trigger data, wrap it in the json function so it
// stays an array rather than being stringified:
//   "prompts": "{{ json $json.prompts }}"

// Full options: prompts, session_id, mode, maxWaitSeconds, downloadDir`,
		Inputs:  "item with prompts array (plus optional session_id, mode)",
		Outputs: "session_id, and the last iteration's response_text / images (every image is vaulted as it is generated)",
		Notes:   "This is what the bundled 'gemimgmany' template uses: monoagentcli workflow templates run gemimgmany --input '{\"prompts\":[...]}'",
	},
	{
		Type:     "gemini.generate_image",
		Category: "gemini",
		Short:    "Generate an image via Gemini — NO API KEY, uses browser session",
		Description: `NO API KEY REQUIRED. Uses browser automation — monoagentcli opens gemini.google.com
in a real Chrome session, submits the prompt, waits for the image to appear,
downloads it to disk, and returns the local file path.

Setup (one-time): run  monoagentcli login gemini  and log in with your Google account.
credential_id is optional — omit it and monoagentcli auto-resolves the saved session.
It requires only a Google login, no API key and no billing.`,
		Config: `{
  "prompt":              "editorial photo of a city skyline at sunset",
  "maxWaitSeconds":      120,
  "downloadDir":         "~/.monoagent/downloads",
  "referenceImagePath":  "/path/to/reference.jpg"
}

// referenceImagePath is OPTIONAL. When set, the image is uploaded to Gemini
// before the prompt is sent. Use it to ask Gemini for "something similar to
// this image but in a different style" — include the style request in prompt.

// credential_id is OPTIONAL — only set it if you have multiple Gemini sessions:
// "credential_id": "social:gemini:gemini-user"`,
		Inputs:  "item with prompt",
		Outputs: "images (array: {path, url, filename, size_bytes}), image_count",
		Notes:   "Use short English prompts (< 100 chars). Long or Persian prompts trigger the Nano Banana 2 reasoning model which cannot generate images. Set referenceImagePath to upload a reference image before the prompt.",
	},
}
