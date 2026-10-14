package catalog

// Reference entries for the data.* nodes added or extended for n8n parity.
// New node types are appended to the shared catalogue; for types already
// documented elsewhere, upsertNodeDocs (catalog.go) replaces that entry with the fuller
// one here so `monoagentcli ref node <type>` shows the current operations.

var dataMoreNodeDocs = []nodeDoc{
	{
		Type:     "data.split_out",
		Category: "data",
		Short:    "Turn a list inside each item into separate items (n8n Split Out)",
		Description: `Emits one item per element of the list in field_to_split_out. Object elements
are merged into the new item; other values are stored under the field name (or
destination_field_name). Several comma-separated fields are split side by side
(element i of each list goes to item i). "$binary" splits an item's binary
files into one item each.`,
		Config: `{
  "field_to_split_out":     "orders",            // comma-separated; dot paths ok
  "include":                "selected_other_fields", // no_other_fields | all_other_fields | selected_other_fields
  "fields_to_include":      "customer_id",
  "destination_field_name": "order",             // optional, one per split field
  "disable_dot_notation":   false,
  "include_binary":         false
}`,
		Inputs:  "items holding a list (or object, whose values are split)",
		Outputs: "one item per list element",
		Notes:   "A missing field yields no items for that input item; a scalar yields one.",
	},
	{
		Type:     "data.summarize",
		Category: "data",
		Short:    "Count, sum, average, min/max, append or concatenate fields across items, optionally grouped (n8n Summarize)",
		Description: `A pivot table over the input. Each aggregation produces <prefix><field>:
count_, unique_count_, sum_, average_, min_, max_, appended_, concatenated_.
sum/average use numeric values only; count/count_unique/append/concatenate skip
null and "" unless include_empty. With fields_to_split_by, one result per group
(separate_items, the group values added as fields) or one nested object
(single_item).`,
		Config: `{
  "fields_to_summarize": [
    { "aggregation": "sum",   "field": "amount" },
    { "aggregation": "count_unique", "field": "customer" },
    { "aggregation": "concatenate", "field": "name", "separate_by": ", " }
  ],
  "fields_to_split_by": "country, city",
  "output_format": "separate_items"     // separate_items | single_item
}`,
		Inputs:  "any items",
		Outputs: "one item per group (or one item in total)",
		Notes:   "fields_to_summarize may also be a JSON string. skip_empty_split_fields drops items without a group value.",
	},
	{
		Type:     "data.rename_keys",
		Category: "data",
		Short:    "Rename item keys by name or regular expression (n8n Rename Keys)",
		Description: `keys renames one key per pair (dot paths reach nested keys, and the new name may
be a path too). regex_replacements then renames every key matching search_regex,
at any depth (depth -1) or down to depth levels; replace_regex may use $1.`,
		Config: `{
  "keys": [ { "current_key": "fname", "new_key": "first_name" } ],
  "regex_replacements": [
    { "search_regex": "^legacy_", "replace_regex": "", "case_insensitive": false, "depth": -1 }
  ]
}`,
		Inputs:  "any items",
		Outputs: "the same items with renamed keys (binary data kept)",
	},
	{
		Type:     "data.jwt",
		Category: "data",
		Short:    "Sign, decode or verify JSON Web Tokens (HS/RS/PS/ES 256-512) (n8n JWT)",
		Description: `sign builds a token from claims (audience, issuer, jwtid and subject become aud,
iss, jti, sub; expires_in / not_before are seconds from now) or from claims_json,
adds iat, and signs it. decode reads a token without checking it. verify checks
the signature, algorithm, exp and nbf.`,
		Config: `{
  "operation":  "sign",                 // sign | decode | verify
  "key_type":   "passphrase",           // passphrase (HS*) | pem_key (RS*/PS*/ES*)
  "secret":     "@secret:jwt-secret",
  "algorithm":  "HS256",
  "claims":     { "subject": "{{ $json.user_id }}", "expires_in": 3600 },
  "token":      "{{ $json.token }}",    // decode / verify
  "complete":   false                   // decode / verify: include header and signature
}`,
		Inputs:  "any items (token and claims_json are evaluated per item)",
		Outputs: "sign: {token}; decode/verify: {payload} or {header, payload, signature}",
		Notes:   "verify options: ignore_expiration, ignore_not_before, clock_tolerance (seconds). PEM keys pasted on one line are accepted.",
	},
	{
		Type:     "data.totp",
		Category: "data",
		Short:    "Generate the current TOTP code from a base32 secret (n8n TOTP)",
		Description: `Computes the RFC 6238 one-time code for now. n8n calls the operation
"Generate Secret"; it outputs the code, not a new secret.`,
		Config: `{
  "secret":    "@secret:github-totp",   // base32
  "label":     "GitHub:octocat",        // optional issuer:username
  "algorithm": "SHA1",                   // SHA1 | SHA224 | SHA256 | SHA384 | SHA512 | SHA3-*
  "digits":    6,
  "period":    30
}`,
		Inputs:  "any items (one result per item)",
		Outputs: "token, secondsRemaining",
	},
	{
		Type:     "data.debug_helper",
		Category: "data",
		Short:    "Throw an error or generate fake test data (n8n DebugHelper)",
		Description: `throw_error fails the node with throw_error_message. random_data generates
random_data_count fake records per input item: user, email, address,
credit_card (Luhn-valid), uuid, mac_address, ipv4, ipv6, lat_long, semver, url,
nanoid. A random_data_seed makes the data repeatable. do_nothing emits no items.`,
		Config: `{
  "category":          "random_data",   // do_nothing | throw_error | random_data
  "random_data_type":  "user",
  "random_data_count": 10,
  "random_data_seed":  "",
  "random_data_single_array": false
}`,
		Inputs:  "any items",
		Outputs: "generated items, or {generatedItems: [...]} with random_data_single_array",
		Notes:   "n8n's out-of-memory category is not supported.",
	},
	{
		Type:     "data.datetime",
		Category: "data",
		Short:    "Parse, format, round, shift and compare dates (incl. n8n Date & Time v2 operations)",
		Description: `Field operations (Go layouts): format, parse, add, subtract, diff, now read
"field" and write "output_field".

n8n Date & Time v2 operations take the date as a value — usually an expression,
evaluated per item — and write output_field_name:
  get_current_date        now (include_time=false: start of today)
  add_to_date / subtract_from_date   magnitude ± duration time_unit (months clamp to month end)
  format_date             Luxon tokens (yyyy-MM-dd HH:mm, X = Unix s, x = Unix ms); from_format reads custom input
  round_date              round_down to_nearest unit, or round_up to the start of the next "to" unit
  get_time_between_dates  start_date → end_date in units (e.g. ["month","day"]); iso_string for P1M2D
  extract_date            year | month | week (ISO) | day | hour | minute | second
Dates may be ISO strings, common date text, or Unix timestamps (seconds or ms).`,
		Config: `{
  "operation":         "add_to_date",
  "magnitude":         "{{ $json.created_at }}",
  "time_unit":         "days",
  "duration":          7,
  "output_field_name": "due",
  "timezone":          "Europe/Berlin",   // default: this machine's zone
  "include_input_fields": true            // default false, as in n8n
}`,
		Inputs:  "any items",
		Outputs: "v2: items with output_field_name (plus input fields when include_input_fields); field ops: input items with output_field",
		Notes:   "Default output names follow n8n: currentDate, newDate, formattedDate, roundedDate, timeDifference, datePart.",
	},
	{
		Type:     "data.crypto",
		Category: "data",
		Short:    "Hash, HMAC, sign, encrypt/decrypt, or generate random values (incl. n8n Crypto actions)",
		Description: `n8n Crypto actions read "value" (evaluated per item) and write data_property_name:
  hash      MD5, SHA1, SHA224, SHA256, SHA384, SHA512, SHA3-224/256/384/512, RIPEMD160 (or a binary property)
  hmac      same algorithms with "secret"
  sign      private_key (RSA PKCS#1 v1.5, ECDSA, Ed25519) with algorithm RSA-SHA256, sha512, …
  generate  uuid, or an ascii/base64/hex string of string_length
  encrypt / decrypt  symmetric: passphrase + cipher (aes-128/192/256-gcm, chacha20-poly1305),
            same payload format as n8n so values move between both; asymmetric: RSA-OAEP
Field operations kept from before: hash_md5, hash_sha256, hash_sha512, hmac_sha256, uuid,
random_bytes, base64_encode, base64_decode (field → output_field).`,
		Config: `{
  "operation": "hash",
  "type":      "SHA256",
  "value":     "{{ $json.email }}",
  "encoding":  "hex",                    // hex | base64
  "data_property_name": "email_hash"
}`,
		Inputs:  "any items",
		Outputs: "input items with the result field set",
		Notes:   "Keep secrets, keys and passphrases in the vault (@secret:name).",
	},
}

// dataFormatsDocOverrides replaces the existing entries of the XML, HTML and
// Markdown nodes, which gained n8n-compatible operations.
var dataFormatsDocOverrides = []nodeDoc{
	{
		Type:     "data.xml",
		Category: "data",
		Short:    "Convert XML to JSON and back (incl. n8n XML node semantics)",
		Description: `xml_to_json parses the XML in data_property_name (default "data") and makes the
parsed object the item, like n8n: attributes merged into their element
(merge_attrs, default true), text of mixed elements under charkey "_", repeated
elements as lists (explicit_array=true makes every child a list), root element
kept (explicit_root). json_to_xml builds XML from the whole item into
data_property_name; "$" holds attributes and "_" text; an object with one key
uses it as the root, otherwise root_name.
parse / generate are the older field-based operations (field -> output_field,
attributes as "@name", text as "#text").`,
		Config: `{
  "operation": "xml_to_json",       // xml_to_json | json_to_xml | parse | generate
  "data_property_name": "data",
  "trim": true, "normalize": false, "explicit_array": false,
  "attrkey": "$", "charkey": "_",
  "headless": false, "root_name": "root", "cdata": false   // json_to_xml
}`,
		Inputs:  "items with an XML string, or any item (json_to_xml)",
		Outputs: "xml_to_json: the parsed object; json_to_xml: {data_property_name: xml}",
		Notes:   "Other options: ignore_attrs, normalize_tags. attrkey/charkey may not be __proto__, constructor or prototype.",
	},
	{
		Type:     "data.html",
		Category: "data",
		Short:    "Extract content with CSS selectors, render HTML templates, or build an HTML table (incl. n8n HTML node)",
		Description: `extract_html_content reads HTML from a JSON field or a binary property
(source_data json|binary, data_property_name) and fills one key per
extraction_values entry: text, html, an attribute, or a form control's value;
return_array gives every match. skip_selectors drops elements from text
results; trim_values and clean_up_text tidy whitespace.
generate_html_template renders "html" with {{ }} expressions per item into "html".
convert_to_html_table turns all items into one {table} (caption, capitalize
headers, custom_styling with table/header/row/cell attributes; values are not
escaped, as in n8n).
extract / extract_all / text / generate are the older field-based operations.`,
		Config: `{
  "operation": "extract_html_content",
  "source_data": "json",
  "data_property_name": "body",
  "extraction_values": [
    { "key": "title", "css_selector": "h1" },
    { "key": "links", "css_selector": "a", "return_value": "attribute", "attribute": "href", "return_array": true }
  ],
  "trim_values": true
}`,
		Inputs:  "items with HTML (field or binary), or any items for templates/tables",
		Outputs: "extracted keys / rendered html / one table item",
	},
	{
		Type:     "data.markdown",
		Category: "data",
		Short:    "Convert Markdown to HTML or HTML to Markdown (incl. n8n Markdown node)",
		Description: `mode markdown_to_html converts "markdown" (a value, evaluated per item) and
mode html_to_markdown converts "html"; the result goes to destination_key
(default "data") on a copy of the item. Instead of a value, "field" can name an
item field to read. Options follow n8n: markdown_to_html tables,
strikethrough, tasklists, simple_line_breaks, no_header_id, open_links_in_new_window,
complete_html_document, …; html_to_markdown heading_style (atx|setext),
bullet_marker, code_block_style (fenced|indented), em_delimiter,
strong_delimiter, ignore (tags), keep_data_images, use_link_reference_definitions.
Without mode the original behaviour applies: Markdown in "field" -> HTML in
"output_field" (GFM).`,
		Config: `{
  "mode": "html_to_markdown",       // markdown_to_html | html_to_markdown
  "html": "{{ $json.body }}",
  "destination_key": "markdown"
}`,
		Inputs:  "any items",
		Outputs: "the item with the converted text at destination_key",
	},
}
