// Node config field definitions, extracted from NodeRunner.jsx to keep that file
// focused on the canvas/editor logic.

import { isAiEdge, mainEdges } from './aiSubNodes.js'

export const NODE_CONFIG_FIELDS = {
  // ── Triggers ──────────────────────────────────────────────────────────────
  'trigger.schedule': [
    { key: 'cron', label: 'Cron Expression', type: 'text', default: '0 0 * * * *',
      help: '6-field cron: sec min hour dom month dow (e.g. "0 0 9 * * *" for 9am daily). 5-field expressions are rejected.' },
    { key: 'timezone', label: 'Timezone', type: 'text', default: 'UTC',
      help: 'IANA timezone name (e.g. "America/New_York"). Leave as UTC to run in server time.' },
    { key: 'overlap', label: 'When a run is still going', type: 'select', options: ['allow','skip','queue_one'], default: 'allow',
      help: 'allow: every tick starts a run. skip: drop the tick while a run is queued, running or waiting. queue_one: keep at most one tick waiting.' },
    { key: 'catch_up', label: 'Missed runs', type: 'select', options: ['none','one','all'], default: 'none',
      help: 'On daemon startup, ticks missed while it was not running: none skips them, one runs once, all runs once per missed tick (at most 100).' },
    { key: 'catch_up_window', label: 'Catch-up window', type: 'text', default: '', placeholder: 'e.g. 6h',
      help: 'Only catch up ticks missed within this long before startup. Empty: no limit.' },
  ],
  'trigger.webhook': [
    { key: 'path', label: 'URL Path', type: 'text', default: '/webhook' },
    { key: 'method', label: 'HTTP Method', type: 'select', options: ['GET','POST','PUT','PATCH','DELETE'], default: 'POST' },
    { key: 'auth_header', label: 'Auth Header Name', type: 'text', default: '',
      help: 'If set, requests must include this header with the auth token value (e.g. X-Webhook-Secret). Leave blank for no auth.' },
    { key: 'auth_token', label: 'Auth Token', type: 'password', default: '',
      help: 'Secret value expected in the auth header.' },
    { key: 'response_mode', label: 'Response Mode', type: 'select', options: ['onReceived','lastNode','responseNode'], default: 'onReceived',
      help: 'onReceived answers at once with the execution_id. lastNode waits for the run and answers with the last node\'s output. responseNode waits for a Respond to Webhook node.' },
    { key: 'response_code', label: 'Response Code', type: 'number', default: '200',
      depends_on: { key: 'response_mode', values: ['onReceived'] } },
    { key: 'dedupe_header', label: 'Delivery ID Header', type: 'text', default: '', placeholder: 'X-GitHub-Delivery',
      help: 'If set, a request whose value of this header was seen in the last 7 days starts no run (e.g. Idempotency-Key).' },
  ],
  'trigger.execute_workflow': [],
  'trigger.error': [],

  // ── Control ───────────────────────────────────────────────────────────────
  'core.if': [
    { key: 'condition', label: 'Condition', type: 'textarea', rows: 2, default: '{{ eq $json.active true }}',
      placeholder: '{{ eq $json.active true }}',
      help: 'Go template expression. Items go to "true" handle if condition passes, "false" otherwise.' },
  ],
  'core.filter': [
    { key: 'condition', label: 'Condition', type: 'textarea', rows: 2, default: '{{ eq $json.status "todo" }}',
      placeholder: '{{ eq $json.status "todo" }}',
      help: 'Go template expression that evaluates to true/false for each item. Use $json.fieldName to access fields.' },
    { key: 'mode', label: 'Mode', type: 'select', options: ['keep','remove'], default: 'keep',
      help: 'keep = pass items where condition is true; remove = pass items where condition is false' },
    { key: '_help', label: 'Condition Reference', type: '_help', helpContent: [
      { title: 'Compare text', examples: ['{{ eq $json.status "todo" }}', '{{ ne $json.status "done" }}'] },
      { title: 'Compare numbers', examples: ['{{ gt $json.age 18 }}', '{{ le $json.price 100 }}'] },
      { title: 'Check if field exists', examples: ['{{ $json.email }}', '{{ ne $json.name "" }}'] },
      { title: 'Contains text', examples: ['{{ contains $json.title "sale" }}', '{{ hasPrefix $json.url "https" }}'] },
      { title: 'AND / OR', examples: ['{{ and (eq $json.status "todo") (ne $json.title "") }}', '{{ or (eq $json.type "A") (eq $json.type "B") }}'] },
      { title: 'Available operators', examples: ['eq (=), ne (!=), gt (>), ge (>=), lt (<), le (<=), and, or, not, contains, hasPrefix, hasSuffix'] },
    ]},
  ],
  'core.switch': [
    { key: 'field', label: 'Field to Switch On', type: 'text', default: '{{$json.status}}' },
    { key: 'cases', label: 'Cases (JSON array)', type: 'textarea', default: '[{"value":"active","handle":"active"},{"value":"inactive","handle":"inactive"}]',
      help: 'Each case is a string (value and output handle) or {"value": …, "handle": …}.' },
    { key: 'default_handle', label: 'Default Handle', type: 'text', default: 'default' },
  ],
  'core.set': [
    { key: 'assignments', label: 'Assignments (JSON)', type: 'textarea', default: '[{"name":"output","value":"{{$json.input}}"}]' },
    { key: 'include_input', label: 'Include Input Fields', type: 'select', options: ['true','false'], default: 'true' },
  ],
  'core.code': [
    { key: 'code', label: 'JavaScript Code', type: 'textarea', default: '// return array of items\nreturn items.map(item => ({ ...item.json }))' },
  ],
  'core.wait': [
    { key: 'resume', label: 'Resume', type: 'select', options: ['duration','timeInterval','specificTime'], default: 'duration',
      help: 'duration waits Wait Duration seconds; timeInterval waits Amount × Unit; specificTime waits until Date and Time.' },
    { key: 'amount', label: 'Amount', type: 'number', default: '1',
      depends_on: { key: 'resume', values: ['timeInterval'] } },
    { key: 'unit', label: 'Unit', type: 'select', options: ['seconds','minutes','hours','days'], default: 'hours',
      depends_on: { key: 'resume', values: ['timeInterval'] } },
    { key: 'dateTime', label: 'Date and Time', type: 'text', default: '', placeholder: '2026-12-31T09:00:00Z',
      depends_on: { key: 'resume', values: ['specificTime'] }, help: 'RFC 3339 or local YYYY-MM-DD HH:MM.' },
    { key: 'duration', label: 'Wait Duration (seconds)', type: 'number', default: '5',
      depends_on: { key: 'resume', values: ['duration', ''] } },
  ],
  'core.limit': [
    { key: 'max_items', label: 'Max Items', type: 'number', default: '10' },
  ],
  'core.split_in_batches': [
    { key: 'batch_size', label: 'Batch Size', type: 'number', default: '10',
      help: '"loop" sends one batch per iteration; wire the end of the loop body back into this node to continue. "done" sends all items once at the end.' },
    { key: 'reset', label: 'Reset', type: 'boolean', default: false,
      help: 'Treat this run\'s input as a new set instead of continuing the current loop (e.g. paging until a page comes back empty).' },
  ],
  'core.sort': [
    { key: 'field', label: 'Field', type: 'text', default: 'name' },
    { key: 'order', label: 'Order', type: 'select', options: ['asc','desc'], default: 'asc' },
    { key: 'type', label: 'Sort Type', type: 'select', options: ['string','number','date'], default: 'string' },
  ],
  'core.remove_duplicates': [
    { key: 'operation', label: 'Operation', type: 'select',
      options: ['removeDuplicateInputItems','removeItemsRepeatedWithinPreviousExecutions','clearDeduplicationHistory'], default: 'removeDuplicateInputItems',
      help: 'removeDuplicateInputItems: drop repeats within this run. removeItemsRepeatedWithinPreviousExecutions: keep only items no earlier run produced. clearDeduplicationHistory: forget the stored values.' },
    { key: 'field', label: 'Field Key', type: 'text', default: 'id' },
    { key: 'keep', label: 'Keep', type: 'select', options: ['first','last'], default: 'first',
      depends_on: { key: 'operation', values: ['removeDuplicateInputItems'] } },
    { key: 'dedupeValue', label: 'Dedupe Value', type: 'text', default: '', placeholder: '{{ $json.link }}',
      depends_on: { key: 'operation', values: ['removeItemsRepeatedWithinPreviousExecutions'] },
      help: 'Value compared with earlier runs. Empty falls back to the field above, then to the whole item.' },
    { key: 'scope', label: 'History Scope', type: 'select', options: ['node','workflow'], default: 'node',
      depends_on: { key: 'operation', values: ['removeItemsRepeatedWithinPreviousExecutions','clearDeduplicationHistory'] },
      help: 'node: this node keeps its own history. workflow: every remove-duplicates node of the workflow shares one.' },
    { key: 'historySize', label: 'History Size', type: 'number', default: '10000',
      depends_on: { key: 'operation', values: ['removeItemsRepeatedWithinPreviousExecutions'] } },
  ],
  'core.execute_workflow': [
    { key: 'workflowId', label: 'Workflow', type: 'text', default: '', placeholder: 'workflow ID or name',
      help: 'The workflow to run. Give it a "When called by another workflow" trigger to receive the items.' },
    { key: 'mode', label: 'Mode', type: 'select', options: ['once','each'], default: 'once',
      help: 'once: one run with all items. each: one run per item.' },
    { key: 'waitForSubWorkflow', label: 'Wait for Sub-workflow', type: 'boolean', default: true,
      help: 'On: wait and output the sub-workflow\'s last node items. Off: pass the input items through with the started execution ID.' },
  ],
  'core.respond_webhook': [
    { key: 'respond_with', label: 'Respond With', type: 'select',
      options: ['firstIncomingItem','allIncomingItems','json','text','noData'], default: 'firstIncomingItem',
      help: 'What the webhook caller gets back when the webhook trigger\'s Response Mode is responseNode.' },
    { key: 'response_code', label: 'Response Code', type: 'number', default: '200' },
    { key: 'response_headers', label: 'Response Headers (JSON)', type: 'textarea', rows: 3, default: '',
      placeholder: '{ "X-Request-Id": "{{ $json.id }}" }' },
    { key: 'response_body', label: 'Response Body', type: 'textarea', rows: 5, default: '',
      depends_on: { key: 'respond_with', values: ['json','text'] },
      help: 'For json: JSON text (expressions allowed). For text: the body as is.' },
  ],
  'core.stop_error': [
    { key: 'message', label: 'Error Message', type: 'text', default: 'Workflow stopped with error' },
  ],
  'core.merge': [
    { key: 'mode', label: 'Mode', type: 'select', options: ['append','first'], default: 'append' },
  ],
  'core.compare_datasets': [
    { key: 'key_field', label: 'Key Field', type: 'text', default: 'id' },
    { key: 'split_at', label: 'Split At Index', type: 'number', default: '' },
  ],
  'core.aggregate': [
    { key: 'group_by', label: 'Group By Field', type: 'text', default: '' },
    { key: 'operations', label: 'Operations (JSON)', type: 'textarea', default: '[{"field":"amount","operation":"sum","output_field":"total"}]' },
  ],

  // ── HTTP ──────────────────────────────────────────────────────────────────
  'http.request': [
    { key: 'method', label: 'Method', type: 'select', options: ['GET','POST','PUT','PATCH','DELETE','HEAD'], default: 'GET' },
    { key: 'url', label: 'URL', type: 'text', default: 'https://api.example.com/endpoint' },
    { key: 'body_type', label: 'Body Type', type: 'select', options: ['none','json','form','raw'], default: 'none' },
    { key: 'body', label: 'Body (JSON)', type: 'textarea', default: '' },
    { key: 'auth_type', label: 'Auth', type: 'select', options: ['none','bearer','basic','api_key'], default: 'none' },
    { key: 'auth_api_key_value', label: 'API Key / Bearer Token', type: 'password', default: '' },
    { key: 'response_format', label: 'Response Format', type: 'select', options: ['json','text','binary'], default: 'json' },
  ],
  'http.ftp': [
    { key: 'host', label: 'Host', type: 'text', default: '' },
    { key: 'port', label: 'Port', type: 'number', default: '21' },
    { key: 'username', label: 'Username', type: 'text', default: '' },
    { key: 'password', label: 'Password', type: 'password', default: '' },
    { key: 'remote_path', label: 'Remote Path', type: 'text', default: '/' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['list','download','upload','delete'], default: 'list' },
  ],
  'http.ssh': [
    { key: 'host', label: 'Host', type: 'text', default: '' },
    { key: 'port', label: 'Port', type: 'number', default: '22' },
    { key: 'username', label: 'Username', type: 'text', default: '' },
    { key: 'password', label: 'Password', type: 'password', default: '' },
    { key: 'command', label: 'Command', type: 'textarea', default: 'echo hello' },
  ],

  // ── System ────────────────────────────────────────────────────────────────
  'system.execute_command': [
    { key: 'command', label: 'Command', type: 'text', default: 'echo' },
    { key: 'args', label: 'Arguments (JSON array)', type: 'text', default: '["hello world"]' },
    { key: 'working_dir', label: 'Working Directory', type: 'text', default: '' },
    { key: 'timeout_seconds', label: 'Timeout (seconds)', type: 'number', default: '30' },
  ],
  'system.rss_read': [
    { key: 'url', label: 'Feed URL', type: 'text', default: 'https://feeds.example.com/rss' },
    { key: 'limit', label: 'Max Items', type: 'number', default: '20' },
  ],

  // ── Data ───────────────────────────────────────────────────────────────────
  'data.datetime': [
    { key: 'operation', label: 'Operation', type: 'select', options: ['format','parse','add','subtract','diff','now'], default: 'now' },
    { key: 'field', label: 'Source Field', type: 'text', default: 'date' },
    { key: 'input_format', label: 'Input Format', type: 'text', default: '' },
    { key: 'output_format', label: 'Output Format', type: 'text', default: '2006-01-02T15:04:05Z07:00' },
    { key: 'duration', label: 'Duration (e.g. 24h, 30m)', type: 'text', default: '' },
    { key: 'output_field', label: 'Output Field', type: 'text', default: '' },
  ],
  'data.crypto': [
    { key: 'operation', label: 'Operation', type: 'select', options: ['md5','sha256','sha512','hmac_sha256','uuid','random_bytes','base64_encode','base64_decode'], default: 'sha256' },
    { key: 'field', label: 'Source Field', type: 'text', default: '' },
    { key: 'key', label: 'HMAC Key', type: 'password', default: '' },
    { key: 'encoding', label: 'Output Encoding', type: 'select', options: ['hex','base64'], default: 'hex' },
  ],
  'data.html': [
    { key: 'operation', label: 'Operation', type: 'select', options: ['extract','extract_all','text','generate'], default: 'extract' },
    { key: 'field', label: 'Source Field', type: 'text', default: '' },
    { key: 'selector', label: 'CSS Selector', type: 'text', default: '' },
    { key: 'attribute', label: 'Attribute', type: 'text', default: '' },
    { key: 'template', label: 'HTML Template', type: 'textarea', default: '' },
  ],
  'data.xml': [
    { key: 'operation', label: 'Operation', type: 'select', options: ['parse','generate'], default: 'parse' },
    { key: 'field', label: 'Source Field', type: 'text', default: '' },
    { key: 'root_element', label: 'Root Element', type: 'text', default: 'root' },
  ],
  'data.markdown': [
    { key: 'field', label: 'Source Field', type: 'text', default: '' },
    { key: 'output_field', label: 'Output Field', type: 'text', default: '' },
  ],
  'data.spreadsheet': [
    { key: 'operation', label: 'Operation', type: 'select', options: ['read_csv','write_csv','read_xlsx','write_xlsx'], default: 'read_csv' },
    { key: 'file_path', label: 'File Path', type: 'text', default: '' },
    { key: 'sheet', label: 'Sheet Name', type: 'text', default: 'Sheet1' },
    { key: 'has_header', label: 'Has Header Row', type: 'select', options: ['true','false'], default: 'true' },
  ],
  'data.compression': [
    { key: 'operation', label: 'Operation', type: 'select', options: ['gzip_compress','gzip_decompress','zip_compress','zip_decompress'], default: 'gzip_compress' },
    { key: 'field', label: 'Source Field (base64)', type: 'text', default: '' },
    { key: 'filename', label: 'Filename (for zip)', type: 'text', default: 'data' },
  ],
  'data.write_binary_file': [
    { key: 'file_path', label: 'Output File Path', type: 'text', default: '' },
    { key: 'field', label: 'Source Field (base64)', type: 'text', default: '' },
  ],

  // ── Database ──────────────────────────────────────────────────────────────
  'db.mysql': [
    { key: 'host', label: 'Host', type: 'text', default: 'localhost' },
    { key: 'port', label: 'Port', type: 'number', default: '3306' },
    { key: 'database', label: 'Database', type: 'text', default: '' },
    { key: 'username', label: 'Username', type: 'text', default: 'root' },
    { key: 'password', label: 'Password', type: 'password', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['query','insert','update','delete'], default: 'query' },
    { key: 'query', label: 'SQL Query', type: 'textarea', default: 'SELECT * FROM users LIMIT 10' },
  ],
  'db.postgres': [
    { key: 'host', label: 'Host', type: 'text', default: 'localhost' },
    { key: 'port', label: 'Port', type: 'number', default: '5432' },
    { key: 'database', label: 'Database', type: 'text', default: '' },
    { key: 'username', label: 'Username', type: 'text', default: 'postgres' },
    { key: 'password', label: 'Password', type: 'password', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['query','insert','update','delete'], default: 'query' },
    { key: 'query', label: 'SQL Query', type: 'textarea', default: 'SELECT * FROM users LIMIT 10' },
  ],
  'db.mongodb': [
    { key: 'connection_string', label: 'Connection String', type: 'text', default: 'mongodb://localhost:27017' },
    { key: 'database', label: 'Database', type: 'text', default: '' },
    { key: 'collection', label: 'Collection', type: 'text', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['find','insertOne','insertMany','updateOne','updateMany','deleteOne','deleteMany','aggregate'], default: 'find' },
    { key: 'filter', label: 'Filter (JSON)', type: 'textarea', default: '{}' },
  ],
  'db.redis': [
    { key: 'addr', label: 'Address', type: 'text', default: 'localhost:6379' },
    { key: 'password', label: 'Password', type: 'password', default: '' },
    { key: 'db', label: 'DB Index', type: 'number', default: '0' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['get','set','del','exists','keys','lpush','rpush','lrange','hset','hget','hgetall'], default: 'get' },
    { key: 'key', label: 'Key', type: 'text', default: '' },
    { key: 'value', label: 'Value', type: 'text', default: '' },
    { key: 'ttl_seconds', label: 'TTL (seconds)', type: 'number', default: '0' },
  ],

  // ── Communication ─────────────────────────────────────────────────────────
  'comm.email_send': [
    { key: 'smtp_host', label: 'SMTP Host', type: 'text', default: 'smtp.gmail.com' },
    { key: 'smtp_port', label: 'SMTP Port', type: 'number', default: '587' },
    { key: 'username', label: 'Username', type: 'text', default: '' },
    { key: 'password', label: 'Password', type: 'password', default: '' },
    { key: 'from', label: 'From', type: 'text', default: '' },
    { key: 'to', label: 'To (comma-separated)', type: 'text', default: '' },
    { key: 'subject', label: 'Subject', type: 'text', default: '' },
    { key: 'body', label: 'Body', type: 'textarea', default: '' },
    { key: 'body_type', label: 'Body Type', type: 'select', options: ['text','html'], default: 'text' },
  ],
  'comm.slack': [
    { key: 'token', label: 'Bot Token', type: 'password', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['post_message','upload_file','get_user_info'], default: 'post_message' },
    { key: 'channel', label: 'Channel', type: 'text', default: '#general' },
    { key: 'text', label: 'Message Text', type: 'textarea', default: '' },
  ],
  'comm.telegram': [
    { key: 'token', label: 'Bot Token', type: 'password', default: '' },
    { key: 'chat_id', label: 'Chat ID', type: 'text', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['send_message','send_photo','send_document'], default: 'send_message' },
    { key: 'text', label: 'Message Text', type: 'textarea', default: '' },
    { key: 'parse_mode', label: 'Parse Mode', type: 'select', options: ['','Markdown','HTML'], default: '' },
  ],
  'comm.discord': [
    { key: 'webhook_url', label: 'Webhook URL', type: 'text', default: '' },
    { key: 'content', label: 'Message', type: 'textarea', default: '' },
    { key: 'username', label: 'Username Override', type: 'text', default: '' },
  ],
  'comm.twilio': [
    { key: 'account_sid', label: 'Account SID', type: 'text', default: '' },
    { key: 'auth_token', label: 'Auth Token', type: 'password', default: '' },
    { key: 'from', label: 'From Number', type: 'text', default: '' },
    { key: 'to', label: 'To Number', type: 'text', default: '' },
    { key: 'body', label: 'Message Body', type: 'textarea', default: '' },
  ],
  'comm.email_read': [
    { key: 'imap_host', label: 'IMAP Host', type: 'text', default: 'imap.gmail.com' },
    { key: 'imap_port', label: 'IMAP Port', type: 'number', default: '993' },
    { key: 'username', label: 'Username', type: 'text', default: '' },
    { key: 'password', label: 'Password', type: 'password', default: '' },
    { key: 'mailbox', label: 'Mailbox', type: 'text', default: 'INBOX' },
    { key: 'limit', label: 'Max Messages', type: 'number', default: '10' },
    { key: 'unread_only', label: 'Unread Only', type: 'select', options: ['true','false'], default: 'false' },
  ],
  'comm.whatsapp': [
    { key: 'access_token', label: 'WhatsApp API Token', type: 'password', default: '' },
    { key: 'phone_number_id', label: 'Phone Number ID', type: 'text', default: '' },
    { key: 'to', label: 'To (E.164 number)', type: 'text', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['send_message','send_template','send_media'], default: 'send_message' },
    { key: 'text', label: 'Message Text', type: 'textarea', default: '' },
    { key: 'template_name', label: 'Template Name', type: 'text', default: '' },
    { key: 'template_language', label: 'Template Language', type: 'text', default: 'en_US' },
    { key: 'media_url', label: 'Media URL', type: 'text', default: '' },
  ],

  // ── Services ──────────────────────────────────────────────────────────────
  'service.github': [
    { key: 'token', label: 'Personal Access Token', type: 'password', default: '' },
    { key: 'owner', label: 'Owner (user/org)', type: 'text', default: '' },
    { key: 'repo', label: 'Repository', type: 'text', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['list_issues','get_issue','create_issue','update_issue','list_prs','list_releases','create_release'], default: 'list_issues' },
    { key: 'number', label: 'Issue / PR Number', type: 'number', default: '' },
    { key: 'title', label: 'Title', type: 'text', default: '' },
    { key: 'body', label: 'Body', type: 'textarea', default: '' },
    { key: 'state', label: 'State Filter', type: 'select', options: ['','open','closed','all'], default: '' },
  ],
  'service.notion': [
    { key: 'token', label: 'Integration Token', type: 'password', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['get_page','create_page','update_page','query_database','create_database','append_blocks'], default: 'get_page' },
    { key: 'page_id', label: 'Page ID', type: 'text', default: '' },
    { key: 'database_id', label: 'Database ID', type: 'text', default: '' },
    { key: 'parent_id', label: 'Parent ID', type: 'text', default: '' },
  ],
  'service.airtable': [
    { key: 'token', label: 'Personal Access Token', type: 'password', default: '' },
    { key: 'base_id', label: 'Base ID', type: 'text', default: '' },
    { key: 'table', label: 'Table Name', type: 'text', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['list','get','create','update','delete'], default: 'list' },
    { key: 'record_id', label: 'Record ID', type: 'text', default: '' },
    { key: 'filter_formula', label: 'Filter Formula', type: 'text', default: '' },
    { key: 'max_records', label: 'Max Records', type: 'number', default: '100' },
    { key: 'view', label: 'View Name', type: 'text', default: '' },
  ],
  'service.google_sheets': [
    { key: 'access_token', label: 'OAuth Access Token', type: 'password', default: '' },
    { key: 'spreadsheet_id', label: 'Spreadsheet ID', type: 'text', default: '' },
    { key: 'sheet', label: 'Sheet Name', type: 'text', default: 'Sheet1' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['read','append','update','clear'], default: 'read' },
    { key: 'range', label: 'Range (e.g. A1:Z)', type: 'text', default: 'A1:Z' },
    { key: 'use_header_row', label: 'Use Header Row', type: 'select', options: ['true','false'], default: 'true' },
    { key: 'value_input_option', label: 'Value Input', type: 'select', options: ['RAW','USER_ENTERED'], default: 'USER_ENTERED' },
  ],
  'service.gmail': [
    { key: 'access_token', label: 'OAuth Access Token', type: 'password', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['send','list','read','trash'], default: 'send' },
    { key: 'to', label: 'To', type: 'text', default: '' },
    { key: 'subject', label: 'Subject', type: 'text', default: '' },
    { key: 'body', label: 'Body', type: 'textarea', default: '' },
    { key: 'body_type', label: 'Body Type', type: 'select', options: ['text','html'], default: 'text' },
    { key: 'query', label: 'Search Query', type: 'text', default: '' },
    { key: 'max_results', label: 'Max Results', type: 'number', default: '20' },
  ],
  'service.google_drive': [
    { key: 'access_token', label: 'OAuth Access Token', type: 'password', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['list','download','upload','delete','create_folder'], default: 'list' },
    { key: 'file_id', label: 'File ID', type: 'text', default: '' },
    { key: 'folder_id', label: 'Folder ID', type: 'text', default: '' },
    { key: 'query', label: 'Search Query', type: 'text', default: '' },
  ],
  'service.jira': [
    { key: 'base_url', label: 'Jira Base URL', type: 'text', default: 'https://yourorg.atlassian.net' },
    { key: 'email', label: 'Email', type: 'text', default: '' },
    { key: 'api_token', label: 'API Token', type: 'password', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['list_issues','get_issue','create_issue','update_issue','add_comment'], default: 'list_issues' },
    { key: 'project_key', label: 'Project Key', type: 'text', default: '' },
    { key: 'issue_key', label: 'Issue Key', type: 'text', default: '' },
    { key: 'issue_type', label: 'Issue Type', type: 'text', default: 'Task' },
    { key: 'summary', label: 'Summary', type: 'text', default: '' },
    { key: 'description', label: 'Description', type: 'textarea', default: '' },
    { key: 'jql', label: 'JQL Query', type: 'text', default: '' },
    { key: 'comment', label: 'Comment', type: 'textarea', default: '' },
  ],
  'service.linear': [
    { key: 'token', label: 'API Key', type: 'password', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['list_issues','get_issue','create_issue','update_issue','list_teams','list_cycles'], default: 'list_issues' },
    { key: 'team_id', label: 'Team ID', type: 'text', default: '' },
    { key: 'issue_id', label: 'Issue ID', type: 'text', default: '' },
    { key: 'title', label: 'Title', type: 'text', default: '' },
    { key: 'description', label: 'Description', type: 'textarea', default: '' },
  ],
  'service.asana': [
    { key: 'token', label: 'Personal Access Token', type: 'password', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['list_tasks','get_task','create_task','update_task','list_projects'], default: 'list_tasks' },
    { key: 'project_id', label: 'Project ID', type: 'text', default: '' },
    { key: 'task_id', label: 'Task ID', type: 'text', default: '' },
    { key: 'name', label: 'Task Name', type: 'text', default: '' },
    { key: 'notes', label: 'Notes', type: 'textarea', default: '' },
  ],
  'service.stripe': [
    { key: 'api_key', label: 'Secret Key', type: 'password', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['list_customers','get_customer','create_customer','list_charges','create_charge','list_subscriptions'], default: 'list_customers' },
    { key: 'customer_id', label: 'Customer ID', type: 'text', default: '' },
    { key: 'limit', label: 'Limit', type: 'number', default: '20' },
  ],
  'service.shopify': [
    { key: 'shop_domain', label: 'Shop Domain', type: 'text', default: 'yourshop.myshopify.com' },
    { key: 'access_token', label: 'Admin API Token', type: 'password', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['list_orders','get_order','list_products','get_product','list_customers'], default: 'list_orders' },
    { key: 'limit', label: 'Limit', type: 'number', default: '50' },
    { key: 'status', label: 'Status Filter', type: 'text', default: '' },
  ],
  'service.salesforce': [
    { key: 'instance_url', label: 'Instance URL', type: 'text', default: 'https://yourorg.salesforce.com' },
    { key: 'access_token', label: 'Access Token', type: 'password', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['query','get','create','update','delete'], default: 'query' },
    { key: 'object_type', label: 'Object Type', type: 'text', default: 'Contact' },
    { key: 'soql', label: 'SOQL Query', type: 'textarea', default: 'SELECT Id, Name FROM Contact LIMIT 10' },
    { key: 'record_id', label: 'Record ID', type: 'text', default: '' },
  ],
  'service.hubspot': [
    { key: 'access_token', label: 'Private App Token', type: 'password', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['list_contacts','get_contact','create_contact','update_contact','list_deals','create_deal'], default: 'list_contacts' },
    { key: 'contact_id', label: 'Contact ID', type: 'text', default: '' },
    { key: 'limit', label: 'Limit', type: 'number', default: '50' },
  ],

  // ── AI ──────────────────────────────────────────────────────────────────
  'ai.choose': [
    { key: 'cases', label: 'Cases', type: 'array', required: true, default: ['billing', 'support', 'other'],
      help: 'Each value becomes an output handle. Objects {value, handle, description} set a custom handle and the criterion Jev judges by.' },
    { key: 'input', label: 'Input', type: 'textarea', rows: 3, default: '', placeholder: '{{$json.text}}',
      help: 'What Jev reads per item ({{$json.field}} placeholders). Empty = the fields below, or the whole item. Capped at 6000 characters.' },
    { key: 'fields', label: 'Fields', type: 'array', help: 'Item keys to send when Input is empty.' },
    { key: 'instructions', label: 'Instructions', type: 'textarea', rows: 3, default: '' },
    { key: 'extra_questions', label: 'Extra Questions (JSON)', type: 'code', language: 'json', default: '',
      placeholder: '{"urgent": {"type": "noul", "criteria": "Is this urgent?"}}',
      help: 'Answered in the same request: {name: {type: noul|choice|score, criteria}}.' },
    { key: 'min_confidence', label: 'Min Probability', type: 'number', default: 0.6,
      help: 'Items below this top probability go to the low_confidence output.' },
    { key: 'output_key', label: 'Output Key', type: 'text', default: 'choice' },
    { key: 'api_key', label: 'TypeSafe API Key', type: 'password', default: '@secret:typesafe' },
    { key: 'model', label: 'Jev Model', type: 'text', default: 'jev-latest' },
    { key: 'concurrency', label: 'Concurrency', type: 'number', default: 4, help: 'Requests in flight (max 8).' },
  ],
  'ai.chat': [
    { key: 'model', label: 'Model', type: 'text', default: '' },
    { key: 'system_prompt', label: 'System Prompt', type: 'textarea', default: '' },
    { key: 'prompt', label: 'Prompt', type: 'textarea', default: '{{$json.text}}' },
    { key: 'temperature', label: 'Temperature', type: 'text', default: '0.7' },
    { key: 'max_tokens', label: 'Max Tokens', type: 'number', default: '1024' },
    { key: 'output_key', label: 'Output Key', type: 'text', default: 'ai_response' },
  ],
  'ai.extract': [
    { key: 'model', label: 'Model', type: 'text', default: '' },
    { key: 'prompt', label: 'Extraction Prompt', type: 'textarea', default: '' },
    { key: 'output_schema', label: 'Output Schema (JSON)', type: 'textarea', default: '' },
    { key: 'temperature', label: 'Temperature', type: 'text', default: '0.2' },
    { key: 'max_tokens', label: 'Max Tokens', type: 'number', default: '1024' },
    { key: 'output_key', label: 'Output Key', type: 'text', default: 'extracted' },
  ],
  'ai.classify': [
    { key: 'model', label: 'Model', type: 'text', default: '' },
    { key: 'categories', label: 'Categories (comma-separated)', type: 'text', default: '' },
    { key: 'prompt_template', label: 'Custom Prompt', type: 'textarea', default: '' },
    { key: 'temperature', label: 'Temperature', type: 'text', default: '0.3' },
    { key: 'max_tokens', label: 'Max Tokens', type: 'number', default: '256' },
  ],
  'ai.transform': [
    { key: 'model', label: 'Model', type: 'text', default: '' },
    { key: 'instruction', label: 'Instruction', type: 'textarea', default: '' },
    { key: 'input_field', label: 'Input Field', type: 'text', default: '' },
    { key: 'temperature', label: 'Temperature', type: 'text', default: '0.5' },
    { key: 'max_tokens', label: 'Max Tokens', type: 'number', default: '1024' },
    { key: 'output_key', label: 'Output Key', type: 'text', default: 'transformed' },
  ],
  'ai.embed': [
    { key: 'model', label: 'Model', type: 'text', default: '' },
    { key: 'input_field', label: 'Input Field', type: 'text', default: '' },
    { key: 'output_key', label: 'Output Key', type: 'text', default: 'embedding' },
  ],
  'ai.agent': [
    { key: 'model', label: 'Model', type: 'text', default: '' },
    { key: 'goal', label: 'Goal', type: 'textarea', default: '' },
    { key: 'max_steps', label: 'Max Steps', type: 'number', default: '5' },
    { key: 'temperature', label: 'Temperature', type: 'text', default: '0.7' },
    { key: 'max_tokens', label: 'Max Tokens', type: 'number', default: '2048' },
  ],

  // ── Marketing / Social API ─────────────────────────────────────────────────
  'service.devto': [
    { key: 'api_key', label: 'API Key', type: 'password', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['list_articles','get_article','publish_article','list_comments','create_comment'], default: 'list_articles' },
    { key: 'article_id', label: 'Article ID', type: 'text', default: '' },
    { key: 'title', label: 'Title', type: 'text', default: '' },
    { key: 'body_markdown', label: 'Body (Markdown)', type: 'textarea', default: '' },
    { key: 'tags', label: 'Tags (comma-separated)', type: 'text', default: '' },
    { key: 'published', label: 'Published', type: 'select', options: ['true','false'], default: 'true' },
  ],
  'service.hashnode': [
    { key: 'token', label: 'Personal Access Token', type: 'password', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['list_posts','get_post','publish_post','list_comments','create_comment'], default: 'list_posts' },
    { key: 'publication_id', label: 'Publication ID', type: 'text', default: '' },
    { key: 'post_id', label: 'Post ID', type: 'text', default: '' },
    { key: 'title', label: 'Title', type: 'text', default: '' },
    { key: 'content_markdown', label: 'Content (Markdown)', type: 'textarea', default: '' },
    { key: 'tags', label: 'Tags (comma-separated)', type: 'text', default: '' },
  ],
  'service.producthunt': [
    { key: 'access_token', label: 'Developer Token', type: 'password', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['list_posts','get_post','create_post','list_comments','create_comment'], default: 'list_posts' },
    { key: 'post_id', label: 'Post ID', type: 'text', default: '' },
    { key: 'topic', label: 'Topic Slug', type: 'text', default: '' },
    { key: 'name', label: 'Product Name', type: 'text', default: '' },
    { key: 'tagline', label: 'Tagline', type: 'text', default: '' },
    { key: 'url', label: 'Product URL', type: 'text', default: '' },
  ],
  'service.bluesky': [
    { key: 'identifier', label: 'Handle or DID', type: 'text', default: '' },
    { key: 'app_password', label: 'App Password', type: 'password', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['create_post','get_timeline','get_profile','list_followers','list_following'], default: 'create_post' },
    { key: 'text', label: 'Post Text', type: 'textarea', default: '' },
    { key: 'actor', label: 'Actor DID / Handle', type: 'text', default: '' },
  ],
  'service.mastodon': [
    { key: 'instance_url', label: 'Instance URL', type: 'text', default: 'https://mastodon.social' },
    { key: 'access_token', label: 'Access Token', type: 'password', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['create_status','get_timeline','get_account','list_followers','list_following'], default: 'create_status' },
    { key: 'status', label: 'Status Text', type: 'textarea', default: '' },
    { key: 'account_id', label: 'Account ID', type: 'text', default: '' },
    { key: 'visibility', label: 'Visibility', type: 'select', options: ['public','unlisted','private','direct'], default: 'public' },
  ],
  'service.reddit': [
    { key: 'client_id', label: 'Client ID', type: 'text', default: '' },
    { key: 'client_secret', label: 'Client Secret', type: 'password', default: '' },
    { key: 'username', label: 'Username', type: 'text', default: '' },
    { key: 'password', label: 'Password', type: 'password', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['submit_post','list_posts','get_post','list_comments','create_comment'], default: 'list_posts' },
    { key: 'subreddit', label: 'Subreddit', type: 'text', default: '' },
    { key: 'title', label: 'Post Title', type: 'text', default: '' },
    { key: 'text', label: 'Post Text / Comment', type: 'textarea', default: '' },
  ],
  'service.discord': [
    { key: 'bot_token', label: 'Bot Token', type: 'password', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['send_message','list_channels','get_guild','list_members'], default: 'send_message' },
    { key: 'channel_id', label: 'Channel ID', type: 'text', default: '' },
    { key: 'guild_id', label: 'Guild (Server) ID', type: 'text', default: '' },
    { key: 'content', label: 'Message Content', type: 'textarea', default: '' },
  ],
  'service.youtube': [
    { key: 'access_token', label: 'OAuth Access Token', type: 'password', default: '' },
    { key: 'operation', label: 'Operation', type: 'select', options: ['list_videos','search','get_channel','list_playlists','list_comments','add_comment'], default: 'list_videos' },
    { key: 'channel_id', label: 'Channel ID', type: 'text', default: '' },
    { key: 'video_id', label: 'Video ID', type: 'text', default: '' },
    { key: 'query', label: 'Search Query', type: 'text', default: '' },
    { key: 'max_results', label: 'Max Results', type: 'number', default: '25' },
    { key: 'text', label: 'Comment Text', type: 'textarea', default: '' },
  ],

  // ── Browser (social) ──────────────────────────────────────────────────────
  'instagram.find_by_keyword': [
    { key: 'keywords', label: 'Keywords (comma-separated)', type: 'text', default: '' },
    { key: 'limit', label: 'Max Results', type: 'number', default: '50' },
    { key: 'username', label: 'Account Username', type: 'text', default: '' },
    { key: 'message', label: 'DM Template', type: 'textarea', default: '' },
  ],
  'instagram.send_dms': [
    { key: 'username', label: 'Account Username', type: 'text', default: '' },
    { key: 'message', label: 'Message Template', type: 'textarea', default: 'Hi {{name}},' },
    { key: 'targets', label: 'Target Usernames (JSON array)', type: 'textarea', default: '["user1","user2"]' },
  ],
  'instagram.follow_users': [
    { key: 'username', label: 'Account Username', type: 'text', default: '' },
    { key: 'targets', label: 'Target Usernames (JSON array)', type: 'textarea', default: '["user1","user2"]' },
    { key: 'limit', label: 'Max Follows', type: 'number', default: '20' },
  ],
  'instagram.publish_post': [
    { key: 'username', label: 'Account Username', type: 'text', default: '' },
    { key: 'text', label: 'Caption', type: 'textarea', default: '' },
    { key: 'media', label: 'Image Path / URL', type: 'text', default: '' },
  ],
  'linkedin.find_by_keyword': [
    { key: 'keywords', label: 'Keywords', type: 'text', default: '' },
    { key: 'limit', label: 'Max Results', type: 'number', default: '50' },
    { key: 'username', label: 'Account Username', type: 'text', default: '' },
  ],
  'linkedin.send_dms': [
    { key: 'username', label: 'Account Username', type: 'text', default: '' },
    { key: 'message', label: 'Message Template', type: 'textarea', default: 'Hi {{name}},' },
    { key: 'targets', label: 'Target Profiles (JSON array)', type: 'textarea', default: '[]' },
  ],
  'linkedin.publish_post': [
    { key: 'username', label: 'Account Username', type: 'text', default: '' },
    { key: 'text', label: 'Post Text', type: 'textarea', default: '' },
    { key: 'media', label: 'Image Path / URL', type: 'text', default: '' },
  ],
  'x.find_by_keyword': [
    { key: 'keywords', label: 'Keywords / Hashtags', type: 'text', default: '' },
    { key: 'limit', label: 'Max Results', type: 'number', default: '50' },
    { key: 'username', label: 'Account Username', type: 'text', default: '' },
  ],
  'x.publish_post': [
    { key: 'username', label: 'Account Username', type: 'text', default: '' },
    { key: 'text', label: 'Post Text', type: 'textarea', default: '' },
  ],
  'tiktok.find_by_keyword': [
    { key: 'keywords', label: 'Keywords / Hashtags', type: 'text', default: '' },
    { key: 'limit', label: 'Max Results', type: 'number', default: '50' },
    { key: 'username', label: 'Account Username', type: 'text', default: '' },
  ],
  'tiktok.publish_post': [
    { key: 'username', label: 'Account Username', type: 'text', default: '' },
    { key: 'text', label: 'Caption', type: 'textarea', default: '' },
    { key: 'media', label: 'Video Path / URL', type: 'text', default: '' },
  ],
}

// Fallback: generic fields for browser nodes not explicitly listed
export const BROWSER_NODE_GENERIC = [
  { key: 'username', label: 'Account Username', type: 'text', default: '' },
  { key: 'targets', label: 'Targets (JSON array)', type: 'textarea', default: '[]' },
  { key: 'limit', label: 'Max Items', type: 'number', default: '20' },
  { key: 'message', label: 'Message / Caption', type: 'textarea', default: '' },
]

// ── Output ports ──────────────────────────────────────────────────────────────
// Handles are not declared in node schemas, so the canvas derives them here.
// Nodes whose handles come from their config (core.switch, ai.choose) must be
// re-derived whenever that config changes.

// parseCaseList accepts an array, a JSON-array string (the textarea fallback
// editor) or a comma-separated string.
function parseCaseList(raw) {
  if (Array.isArray(raw)) return raw
  if (typeof raw !== 'string' || !raw.trim()) return []
  const s = raw.trim()
  if (s.startsWith('[')) {
    try { const v = JSON.parse(s); return Array.isArray(v) ? v : [] } catch { return [] }
  }
  return s.split(',').map(x => x.trim()).filter(Boolean)
}

// arrayFieldItems is what the tag editor of an `array` field shows: arrays
// as-is, a JSON-array string parsed (a comma split would shred it), anything
// else comma-split.
export function arrayFieldItems(raw) {
  if (Array.isArray(raw)) return raw
  if (raw == null || raw === '') return []
  const s = String(raw).trim()
  if (s.startsWith('[')) {
    try { const v = JSON.parse(s); if (Array.isArray(v)) return v } catch { /* not JSON: split below */ }
  }
  return s.split(',').map(x => x.trim()).filter(Boolean)
}

// arrayTagLabel renders one array item as text. Case lists hold objects
// ({value, handle, description}) — rendering those as a React child crashes
// the whole page.
export function arrayTagLabel(item) {
  if (item == null) return ''
  if (typeof item === 'object') { try { return JSON.stringify(item) } catch { return String(item) } }
  return String(item)
}

// normalizeConfigForSave turns a JSON-array string `cases` (the textarea
// fallback editor) into a real array: core.switch only reads a list, so a
// string would silently route every item to the default handle.
export function normalizeConfigForSave(type, config) {
  const cfg = config || {}
  if ((type === 'core.switch' || type === 'ai.choose') && typeof cfg.cases === 'string' && cfg.cases.trim().startsWith('[')) {
    try { const v = JSON.parse(cfg.cases); if (Array.isArray(v)) return { ...cfg, cases: v } } catch { /* keep as typed */ }
  }
  return cfg
}

// caseHandles mirrors the backends: a string case is its own handle; an
// object uses `handle` (core.switch skips objects without one, ai.choose
// falls back to `value`). Duplicates collapse, order is kept.
export function caseHandles(raw, { handleFallsBackToValue = false } = {}) {
  const out = []
  for (const c of parseCaseList(raw)) {
    let h = ''
    if (typeof c === 'string') {
      h = c.trim()
      // Tag inputs store raw text; a pasted JSON object still counts.
      if (h.startsWith('{')) {
        try { const o = JSON.parse(h); h = String(o.handle || (handleFallsBackToValue ? o.value ?? '' : '')).trim() } catch { /* plain text */ }
      }
    } else if (c && typeof c === 'object') {
      h = String(c.handle || (handleFallsBackToValue && c.value != null ? c.value : '')).trim()
    }
    if (h && !out.includes(h)) out.push(h)
  }
  return out
}

const port = id => ({ id, label: id })

export function deriveInputs(type) {
  if (type.startsWith('trigger.')) return []
  return [{ id: 'in', label: 'in' }]
}

export function deriveOutputs(type, config = {}) {
  const cfg = config || {}
  if (type === 'core.if')                return [port('true'), port('false')]
  if (type === 'core.switch') {
    const def = (typeof cfg.default_handle === 'string' && cfg.default_handle.trim()) || 'default'
    const hs = caseHandles(cfg.cases)
    if (!hs.includes(def)) hs.push(def)
    return hs.map(port)
  }
  if (type === 'ai.choose') {
    const hs = caseHandles(cfg.cases, { handleFallsBackToValue: true }).filter(h => h !== 'low_confidence')
    return [...hs, 'low_confidence'].map(port)
  }
  if (type === LOOP_NODE_TYPE)           return [port('loop'), port('done')]
  if (type === 'core.filter')            return [port('main'), port('rejected')]
  if (type === 'core.human_in_loop')     return [port('main'), port('rejected')]
  if (type === 'core.merge')             return [port('out')]
  if (type === 'core.stop_error')        return []
  if (type === 'trigger.webhook')        return [port('body'), port('headers')]
  if (type === 'system.execute_command') return [port('stdout'), port('stderr')]
  if (type.startsWith('db.'))            return [port('rows'), port('error')]
  if (type.startsWith('http.'))          return [port('out'), port('error')]
  return [port('main')]
}

// LOOP_NODE_TYPE is the one node type that may close a cycle (n8n's "Loop
// Over Items"): its "loop" output feeds the loop body, an edge from the body
// back into it starts the next iteration, "done" fires once at the end.
export const LOOP_NODE_TYPE = 'core.split_in_batches'

// canonicalOutputHandle maps a legacy handle to its current port id: loop
// nodes saved before the rename used "batch" for "loop" (the engine still
// accepts both).
export function canonicalOutputHandle(type, handle) {
  return type === LOOP_NODE_TYPE && handle === 'batch' ? 'loop' : handle
}

const isLoopHandle = h => h === 'loop' || h === 'batch'

// loopBackEdges mirrors workflow.BuildDAG (dag_loop.go): an edge u→L into a
// loop node L is a back-edge when u is reachable from L's loop output
// without passing through L itself. Returns a Map from each such edge to
// the ids of the nodes its loop runs through (L and its body).
export function loopBackEdges(nodes, allEdges) {
  const edges = mainEdges(allEdges)
  const byId = new Map((nodes || []).map(n => [n.id, n]))
  const handleOf = e => e.sourcePortId ?? byId.get(e.source)?.outputs?.[e.sourcePortIdx]?.id
  const out = new Map()
  for (const e of edges || []) {
    if (!out.has(e.source)) out.set(e.source, [])
    out.get(e.source).push(e)
  }
  const back = new Map()
  for (const n of nodes || []) {
    if (n.subtype !== LOOP_NODE_TYPE) continue
    const seen = new Set()
    const queue = (out.get(n.id) || []).filter(e => isLoopHandle(handleOf(e))).map(e => e.target)
    queue.forEach(t => seen.add(t))
    while (queue.length) {
      const cur = queue.shift()
      if (cur === n.id) continue
      for (const e of out.get(cur) || []) if (!seen.has(e.target)) { seen.add(e.target); queue.push(e.target) }
    }
    seen.add(n.id)
    for (const e of edges || []) {
      if (e.target === n.id && (e.source === n.id || seen.has(e.source))) back.set(e, seen)
    }
  }
  return back
}

// LOOP_EDGE_OFF is how far right of the source port (and left of the target
// port) a loop back-edge turns down/up: close to the port, so it stays clear
// of the curves of neighbouring edges in the gap between two nodes.
export const LOOP_EDGE_OFF = 12

// loopEdgeBottom is the y of a loop back-edge's bottom run: below the
// loop's own nodes, then pushed further down past any node box ({id, x, y,
// w, h}) the run between x1 and x2 would otherwise cross.
export function loopEdgeBottom(boxes, loopIds, x1, x2, margin = 28) {
  return loopEdgeRun(boxes, loopIds, x1, x2, margin, 1)
}

// loopEdgeTop is loopEdgeBottom's mirror: a run above the loop's nodes.
export function loopEdgeTop(boxes, loopIds, x1, x2, margin = 28) {
  return loopEdgeRun(boxes, loopIds, x1, x2, margin, -1)
}

function loopEdgeRun(boxes, loopIds, x1, x2, margin, side) {
  const lo = Math.min(x1, x2), hi = Math.max(x1, x2)
  const own = boxes.filter(b => loopIds.has(b.id))
  let y = side > 0
    ? (own.length ? Math.max(...own.map(b => b.y + b.h)) : 0) + margin
    : (own.length ? Math.min(...own.map(b => b.y)) : 0) - margin
  const crossed = boxes.filter(b => b.x < hi && b.x + b.w > lo)
  for (let moved = true; moved;) {
    moved = false
    for (const b of crossed) {
      if (y > b.y - margin && y < b.y + b.h + margin) { y = side > 0 ? b.y + b.h + margin : b.y - margin; moved = true }
    }
  }
  return y
}

// edgePath is an ordinary edge: a horizontal-tangent cubic bezier.
export function edgePath(sx, sy, tx, ty) {
  const dx = Math.max(60, Math.abs(tx - sx) * 0.5)
  return `M${sx},${sy} C${sx+dx},${sy} ${tx-dx},${ty} ${tx},${ty}`
}

// edgePoints samples n+1 points along edgePath's curve.
export function edgePoints(sx, sy, tx, ty, n = 24) {
  const dx = Math.max(60, Math.abs(tx - sx) * 0.5)
  const p = [[sx, sy], [sx + dx, sy], [tx - dx, ty], [tx, ty]]
  const out = []
  for (let i = 0; i <= n; i++) {
    const t = i / n, u = 1 - t
    const w = [u * u * u, 3 * u * u * t, 3 * u * t * t, t * t * t]
    out.push([w.reduce((a, k, j) => a + k * p[j][0], 0), w.reduce((a, k, j) => a + k * p[j][1], 0)])
  }
  return out
}

// chooseLoopEdgeY picks where a loop back-edge from (sx,sy) to (tx,ty) runs:
// below its loop or above it, whichever route has fewer obstacle points
// ([x, y]: other edges' sampled curves, ports of nodes outside the loop)
// within clearance of its segments. A tie keeps the route below.
export function chooseLoopEdgeY({ sx, sy, tx, ty, boxes, loopIds, obstacles = [], off = LOOP_EDGE_OFF, clearance = 16 }) {
  const a = sx + off, b = tx - off
  const near = (y) => {
    const segs = [[a, sy, a, y], [Math.min(a, b), y, Math.max(a, b), y], [b, y, b, ty]]
    return obstacles.filter(([px, py]) => segs.some(([x1, y1, x2, y2]) => {
      const cx = Math.max(Math.min(x1, x2), Math.min(px, Math.max(x1, x2)))
      const cy = Math.max(Math.min(y1, y2), Math.min(py, Math.max(y1, y2)))
      return Math.hypot(px - cx, py - cy) < clearance
    })).length
  }
  const below = loopEdgeBottom(boxes, loopIds, a, b)
  const above = loopEdgeTop(boxes, loopIds, a, b)
  return near(above) < near(below) ? above : below
}

// loopEdgePath routes a loop back-edge around its loop instead of across it:
// out of the source port, down (or up) to runY clear of the loop's nodes,
// back along it, and into the target port, with rounded corners.
export function loopEdgePath(sx, sy, tx, ty, runY, off = LOOP_EDGE_OFF, r = 8) {
  const a = sx + off, b = tx - off
  const dir = b < a ? -1 : 1
  const down = runY < sy ? -1 : 1, up = ty < runY ? -1 : 1
  return `M${sx},${sy} H${a - r} Q${a},${sy} ${a},${sy + down * r} V${runY - down * r} Q${a},${runY} ${a + dir * r},${runY}` +
    ` H${b - dir * r} Q${b},${runY} ${b},${runY + up * r} V${ty - up * r} Q${b},${ty} ${b + r},${ty} H${tx}`
}

// hasForbiddenCycle reports whether the graph has a cycle other than a loop
// back-edge into a loop node — the engine rejects such a workflow. AI
// sub-node connections are not part of the flow and never count.
export function hasForbiddenCycle(nodes, edges) {
  const back = loopBackEdges(nodes, edges)
  const adj = new Map()
  for (const e of edges || []) {
    if (back.has(e) || isAiEdge(e)) continue
    if (!adj.has(e.source)) adj.set(e.source, [])
    adj.get(e.source).push(e.target)
  }
  const state = new Map() // 1 = on the DFS stack, 2 = done
  const visit = id => {
    state.set(id, 1)
    for (const t of adj.get(id) || []) {
      if (state.get(t) === 1) return true
      if (!state.has(t) && visit(t)) return true
    }
    state.set(id, 2)
    return false
  }
  for (const id of adj.keys()) if (!state.has(id) && visit(id)) return true
  return false
}

// portsDependOnConfig reports whether a config key change can move ports.
export function portsDependOnConfig(type, key) {
  if (type === 'core.switch') return key === 'cases' || key === 'default_handle'
  if (type === 'ai.choose') return key === 'cases'
  return false
}

// isCaseListSettled is false while a JSON-array string is mid-edit (does not
// parse yet) — callers keep the current ports instead of dropping edges.
export function isCaseListSettled(raw) {
  if (typeof raw !== 'string') return true
  const s = raw.trim()
  if (!s.startsWith('[')) return true
  try { JSON.parse(s); return true } catch { return false }
}

// portRenames detects an in-place rename: same port count with exactly one
// id changed at the same position (typing a new default_handle, or editing
// one case's handle in the JSON editor). Every keystroke re-derives ports,
// so without this the first key typed would drop that port's edges.
export function portRenames(prevOutputs, outputs) {
  if (!Array.isArray(prevOutputs) || !Array.isArray(outputs) || prevOutputs.length !== outputs.length) return {}
  const diff = []
  prevOutputs.forEach((p, i) => { if (p?.id !== outputs[i]?.id) diff.push(i) })
  if (diff.length !== 1) return {}
  const i = diff[0]
  if (outputs.some(p => p.id === prevOutputs[i].id)) return {} // moved, not renamed
  return { [prevOutputs[i].id]: outputs[i].id }
}

// remapSourceEdges re-points a node's outgoing edges at its new ports by
// handle id (following an in-place rename when prevOutputs is given); edges
// whose handle no longer exists are dropped (the backend would never route
// to them).
export function remapSourceEdges(edges, nodeId, outputs, prevOutputs) {
  const renames = portRenames(prevOutputs, outputs)
  return edges.flatMap(e => {
    if (e.source !== nodeId) return [e]
    let id = e.sourcePortId
    // Legacy files stored numeric handles: resolve those by position.
    if (id == null || id === '' || /^\d+$/.test(String(id))) id = (prevOutputs || outputs)[e.sourcePortIdx]?.id
    if (Object.prototype.hasOwnProperty.call(renames, id)) id = renames[id]
    const idx = outputs.findIndex(p => p.id === id)
    if (idx === -1) return []
    return [idx === e.sourcePortIdx && id === e.sourcePortId ? e : { ...e, sourcePortIdx: idx, sourcePortId: id }]
  })
}

// makeIdGen hands out canvas ids (`${prefix}1`, `${prefix}2`, …). A loaded
// workflow already holds such ids: advancePast moves the counter beyond
// them, and next skips any id in taken.
export function makeIdGen(prefix) {
  let seq = 1
  const re = new RegExp(`^${prefix}(\\d+)$`)
  return {
    next(taken) {
      let id
      do { id = `${prefix}${seq++}` } while (taken && taken.has(id))
      return id
    },
    advancePast(ids) {
      for (const id of ids || []) {
        const m = re.exec(String(id))
        if (m) seq = Math.max(seq, Number(m[1]) + 1)
      }
    },
  }
}
