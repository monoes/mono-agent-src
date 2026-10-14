package guardrails

// GuardrailsNodeSchema documents the config keys Node.Execute reads — see
// internal/nodes/control.SetNodeSchema's doc comment for why this is a
// companion struct, and internal/tools/schemagen for the tag grammar.
//
// classify sends each item to the pass or fail handle; sanitize sends it
// to main with the text's findings replaced by <TYPE> placeholders.
// Keywords and the model-judged checks (jailbreak, nsfw, topical
// alignment, custom) only classify.
type GuardrailsNodeSchema struct {
	Operation string `json:"operation" schema:"label=Operation,type=select,default=classify,options=classify|sanitize,help=classify: items go to pass or fail. sanitize: personal data， secret keys， URLs and custom patterns are replaced with <TYPE> placeholders."`

	Text string `json:"text" schema:"label=Text To Check,type=textarea,required,rows=2,default={{ $json.chatInput }},help=Resolved per item. The result is in guardrailsInput."`

	Keywords string `json:"keywords" schema:"label=Keywords,type=array,item_type=text,help=classify: fail when any of these appears as a whole word (case-insensitive)."`

	Jailbreak bool `json:"jailbreak" schema:"label=Jailbreak,type=boolean,default=false,help=classify: a model judges whether the text tries to get around an AI's instructions or safety rules."`

	JailbreakThreshold float64 `json:"jailbreak_threshold" schema:"label=Jailbreak Threshold,type=number,default=0.7,min=0,max=1,depends_on_key=jailbreak,depends_on_values=true"`

	JailbreakPrompt string `json:"jailbreak_prompt" schema:"label=Jailbreak Prompt,type=textarea,rows=4,depends_on_key=jailbreak,depends_on_values=true,help=Empty = the built-in prompt."`

	NSFW bool `json:"nsfw" schema:"label=NSFW,type=boolean,default=false,help=classify: a model judges whether the text is not safe for work."`

	NSFWThreshold float64 `json:"nsfw_threshold" schema:"label=NSFW Threshold,type=number,default=0.7,min=0,max=1,depends_on_key=nsfw,depends_on_values=true"`

	NSFWPrompt string `json:"nsfw_prompt" schema:"label=NSFW Prompt,type=textarea,rows=4,depends_on_key=nsfw,depends_on_values=true,help=Empty = the built-in prompt."`

	PII bool `json:"pii" schema:"label=Personal Data (PII),type=boolean,default=false,help=Find personal data: e-mail， phone， credit card (Luhn)， IBAN， IP， SSN and national IDs."`

	PIIEntities string `json:"pii_entities" schema:"label=PII Entities,type=array,item_type=text,depends_on_key=pii,depends_on_values=true,help=Empty = all. n8n names: EMAIL_ADDRESS， PHONE_NUMBER， CREDIT_CARD， IBAN_CODE， IP_ADDRESS， US_SSN， DATE_TIME， LOCATION， … (see ref node ai.guardrails)."`

	SecretKeys bool `json:"secret_keys" schema:"label=Secret Keys,type=boolean,default=false,help=Find API keys， tokens， private keys and random-looking credentials."`

	SecretKeysPermissiveness string `json:"secret_keys_permissiveness" schema:"label=Secret Keys Permissiveness,type=select,default=balanced,options=strict|balanced|permissive,depends_on_key=secret_keys,depends_on_values=true,help=strict flags more (also short random strings)， permissive only known key formats and long random strings."`

	TopicalAlignment bool `json:"topical_alignment" schema:"label=Topical Alignment,type=boolean,default=false,help=classify: a model judges whether the text strays from the business scope."`

	TopicalAlignmentPrompt string `json:"topical_alignment_prompt" schema:"label=Topical Alignment Prompt,type=textarea,rows=4,depends_on_key=topical_alignment,depends_on_values=true,placeholder=You check whether text stays within this business scope: support for our accounting software. Flag anything else.,help=Required with topical alignment: describe the allowed scope."`

	TopicalAlignmentThreshold float64 `json:"topical_alignment_threshold" schema:"label=Topical Alignment Threshold,type=number,default=0.7,min=0,max=1,depends_on_key=topical_alignment,depends_on_values=true"`

	URLs bool `json:"urls" schema:"label=URLs,type=boolean,default=false,help=Block URLs that are not in the allow list."`

	AllowedURLs string `json:"allowed_urls" schema:"label=Allowed URLs,type=array,item_type=text,depends_on_key=urls,depends_on_values=true,help=Domains， URLs (path prefix)， IPs or CIDRs. Empty = block every URL."`

	AllowedSchemes string `json:"allowed_schemes" schema:"label=Allowed Schemes,type=array,item_type=text,depends_on_key=urls,depends_on_values=true,help=Default https. Also http， ftp， data， javascript， vbscript， mailto."`

	BlockUserinfo bool `json:"block_userinfo" schema:"label=Block Userinfo,type=boolean,default=true,depends_on_key=urls,depends_on_values=true,help=Block URLs with user:pass@ (sanitize replaces them)."`

	AllowSubdomains bool `json:"allow_subdomains" schema:"label=Allow Subdomains,type=boolean,default=true,depends_on_key=urls,depends_on_values=true"`

	Custom string `json:"custom" schema:"label=Custom Guardrails (JSON),type=code,language=json,rows=4,placeholder=[{\"name\": \"competitors\"， \"prompt\": \"Flag text that mentions a competitor.\"， \"threshold\": 0.7}],help=classify: model-judged checks of your own."`

	CustomRegex string `json:"custom_regex" schema:"label=Custom Regex (JSON),type=code,language=json,rows=4,placeholder=[{\"name\": \"ORDER_ID\"， \"pattern\": \"/ORD-[0-9]{6}/i\"}],help=Matches fail classify; sanitize replaces them with <name>. Go RE2 syntax， /pattern/flags or plain."`

	SystemMessage string `json:"system_message" schema:"label=System Message,type=textarea,rows=4,help=Replaces the built-in rules appended after every model-judged check's prompt. Empty = built-in."`

	Runtime string `json:"runtime" schema:"label=Agent Runtime,type=text,help=For the model-judged checks: claude， codex， opencode， … or auto (see agent.ask)."`

	Model string `json:"model" schema:"label=Model,type=text,help=Falls back to the runtime's default model."`

	Timeout float64 `json:"timeout" schema:"label=Timeout (seconds),type=number,default=120,help=Per model turn."`
}
