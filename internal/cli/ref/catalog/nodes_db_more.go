package catalog

// Reference entries for the database, queue and directory nodes (n8n
// parity). catalog.go replaces the entries of types already documented with
// these and appends the new types.

const sqlN8nOps = `n8n-compatible operations (run once per input item):
  executeQuery  run "query"; $1..$N come from "query_replacement" (values,
                comma list or JSON array) — on every database, not just Postgres
  select        table + conditions [{column, condition, value}] (equal, !=,
                LIKE, >, <, >=, <=, IS NULL, IS NOT NULL), combine_conditions
                AND|OR, sort [{column, direction}], limit / return_all
  insert        data_mode auto_map (item fields → columns, narrowed by
                "columns") or define ("values" object)
  update        same row data; "match_columns" picks the key column(s)
                ("value_to_match_on" sets the key in define mode)
  upsert        insert-or-update on "match_columns"
  delete        rows where match_columns equal the item's fields (or conditions)
  deleteTable   delete_command truncate | delete (with conditions) | drop;
                cascade, restart_sequences (Postgres)
Options: output_columns (RETURNING on Postgres-family), skip_on_conflict,
replace_empty_strings, query_batching single|transaction (one transaction,
first error fails the node) | independently (failures go to the "error"
output). Identifiers are quoted for the dialect; values are always bound.`

var dbNodeDocs = []nodeDoc{
	{
		Type:     "db.postgres",
		Category: "db",
		Short:    "Query or write to a PostgreSQL database",
		Description: `Legacy mode (unchanged): operation select|insert|update|delete|execute with a
raw "query" + "params" ($1…), or "table" + "data" / "where", runs once.
The n8n-compatible mode starts as soon as an n8n-only operation
(executeQuery, upsert, deleteTable) or any n8n field below is used.

` + sqlN8nOps,
		Config: `{
  "credential_id": "my-pg",
  "operation":     "upsert",
  "schema":        "public",
  "table":         "users",
  "data_mode":     "auto_map",
  "match_columns": "email",
  "output_columns": "id, email"
}`,
		Inputs:  "items; their fields become columns (auto_map) or feed {{ }} expressions",
		Outputs: "one item per returned row; {success, rows_affected} for statements without rows",
		Notes:   "Legacy select/execute: one item per row / {rows_affected, last_insert_id}.",
	},
	{
		Type:     "db.mysql",
		Category: "db",
		Short:    "Query or write to a MySQL / MariaDB database",
		Description: `Legacy mode as db.postgres, with ? placeholders in "params".
` + sqlN8nOps + `
MySQL has no RETURNING: writes output {success, rows_affected, last_insert_id}.
upsert uses INSERT … ON DUPLICATE KEY UPDATE (match_columns must be a unique key).`,
		Config:  `{ "credential_id": "my-mysql", "operation": "executeQuery", "query": "SELECT * FROM orders WHERE status = $1", "query_replacement": "{{ $json.status }}" }`,
		Inputs:  "items",
		Outputs: "rows, or {success, rows_affected, last_insert_id}",
	},
	{
		Type:     "db.mssql",
		Category: "db",
		Short:    "Query or write to Microsoft SQL Server / Azure SQL",
		Description: `connection_string: sqlserver://user:pass@host:1433?database=db (ADO strings work).
Pure-Go driver. Upsert uses MERGE; select limit uses TOP.
` + sqlN8nOps,
		Config:  `{ "credential_id": "my-mssql", "operation": "insert", "table": "dbo.leads", "columns": "name, email" }`,
		Inputs:  "items",
		Outputs: "rows, or {success, rows_affected}",
	},
	{
		Type:     "db.oracle",
		Category: "db",
		Short:    "Query or write to an Oracle Database (no Instant Client needed)",
		Description: `connection_string: oracle://user:pass@host:1521/service_name (pure-Go go-ora).
Plain identifiers stay unquoted (Oracle upper-cases them); others are quoted.
"bind_params" [{name, value}] binds :name parameters in executeQuery.
` + sqlN8nOps,
		Config:  `{ "credential_id": "my-oracle", "query": "SELECT * FROM emp WHERE deptno = :dept", "bind_params": [{ "name": "dept", "value": 10 }] }`,
		Inputs:  "items",
		Outputs: "rows, or {success, rows_affected}",
	},
	{
		Type:     "db.snowflake",
		Category: "db",
		Short:    "Run SQL on Snowflake through the SQL REST API",
		Description: `Uses /api/v2/statements (no driver; long queries are polled, result
partitions fetched). Connection: account, plus private_key (key-pair JWT,
with username) or token + token_type OAUTH | PROGRAMMATIC_ACCESS_TOKEN;
optional database, schema, warehouse, role. Plain passwords are not accepted
by the SQL API — create a programmatic access token instead.
` + sqlN8nOps,
		Config:  `{ "credential_id": "my-snowflake", "operation": "executeQuery", "query": "SELECT * FROM orders LIMIT 10" }`,
		Inputs:  "items",
		Outputs: "typed rows (numbers, booleans, VARIANT as JSON)",
	},
	{
		Type:        "db.timescaledb",
		Category:    "db",
		Short:       "Query or write to TimescaleDB (Postgres driver)",
		Description: "Same operations and options as db.postgres's n8n mode.\n" + sqlN8nOps,
		Config:      `{ "credential_id": "my-timescale", "operation": "insert", "table": "metrics", "columns": "time, device, value" }`,
		Inputs:      "items",
		Outputs:     "returned rows (RETURNING *)",
	},
	{
		Type:        "db.cratedb",
		Category:    "db",
		Short:       "Query or write to CrateDB (Postgres wire protocol)",
		Description: "deleteTable truncate is DELETE FROM (CrateDB has no TRUNCATE options).\n" + sqlN8nOps,
		Config:      `{ "credential_id": "my-crate", "operation": "insert", "schema": "doc", "table": "events" }`,
		Inputs:      "items",
		Outputs:     "returned rows",
	},
	{
		Type:        "db.questdb",
		Category:    "db",
		Short:       "Query or write to QuestDB (Postgres wire protocol)",
		Description: "No RETURNING and no upsert in QuestDB: writes output {success, rows_affected}.\n" + sqlN8nOps,
		Config:      `{ "credential_id": "my-questdb", "operation": "insert", "table": "trades" }`,
		Inputs:      "items",
		Outputs:     "rows, or {success, rows_affected}",
	},
	{
		Type:     "db.mongodb",
		Category: "db",
		Short:    "Read or write documents in MongoDB",
		Description: `Legacy operations (unchanged, run once): find (filter, limit), insert_one,
insert_many, update_one, update_many, delete_one, delete_many, aggregate
(pipeline).
n8n-compatible operations (once per item; also used by find/aggregate when
"query" is set): find (query, query_parameters for "$1".. strings, limit,
skip, sort, projection), aggregate (query = pipeline array), delete
(deleteMany → {deletedCount}), insert (fields from the item; output
item + id), update / findOneAndUpdate ($set by update_key, upsert),
findOneAndReplace, listSearchIndexes, createSearchIndex, updateSearchIndex,
dropSearchIndex (index_name, index_definition, index_type search|vectorSearch).
Queries accept Extended JSON ($oid, $date); key order is kept.`,
		Config:  `{ "credential_id": "my-mongo", "database": "app", "collection": "users", "operation": "update", "update_key": "email", "fields": "name,plan", "upsert": true }`,
		Inputs:  "items",
		Outputs: "documents (ObjectIDs as hex strings)",
	},
	{
		Type:     "db.redis",
		Category: "db",
		Short:    "Read and write Redis keys, lists and channels",
		Description: `Legacy operations (unchanged, run once): get, set, del, exists, expire, hget,
hset, hgetall, lpush, lrange, keys, incr.
n8n-compatible operations (once per item; switch on with n8n_compat or any
n8n field): get (key_type automatic|string|hash|list|sets → property_name),
set (key_type, value_is_json, expire + ttl), delete, incr ({key: value}),
keys (key_pattern, get_values), llen, push / pop (list, tail), publish
(channel, message_data), info (parsed INFO output).`,
		Config:  `{ "credential_id": "my-redis", "operation": "push", "list": "jobs", "message_data": "{{ $json }}", "tail": true }`,
		Inputs:  "items",
		Outputs: "per operation; set/delete/push/publish pass the input item through",
	},
	{
		Type:     "db.ldap",
		Category: "db",
		Short:    "Search and change entries in an LDAP / Active Directory server",
		Description: `Operations (once per item): search (base_dn, search_for "(objectclass=…)" or
custom + custom_filter, attribute + search_text with * wildcards, scope
sub|one|base, return_all / limit, return_attributes, page_size), compare
(dn, attribute_id, value), create (dn, attributes [{id, value}]), update
(dn, attributes {add|replace|delete: [{id, value}]}), rename (dn →
target_dn), delete (dn). Connection: hostname, port, bind_dn,
bind_password, connection_security none|tls|startTLS.`,
		Config:  `{ "credential_id": "corp-ldap", "operation": "search", "base_dn": "dc=corp,dc=com", "search_for": "(objectclass=person)", "attribute": "mail", "search_text": "*@corp.com" }`,
		Inputs:  "items",
		Outputs: "one item per entry: dn plus attributes (multi-valued as arrays, binary as base64)",
	},
	{
		Type:     "queue.kafka",
		Category: "db", // listed under DB: ref nodes prints a fixed category list
		Short:    "Produce a Kafka record per item",
		Description: `Connection: brokers, client_id, ssl/ca/cert/key, username/password +
sasl_mechanism plain|scram-sha-256|scram-sha-512.
Config: topic, send_input_data (default true; else "message"), message_key,
headers, acks (all replicas), compression none|gzip|snappy|lz4|zstd, timeout (ms).`,
		Config:  `{ "credential_id": "my-kafka", "topic": "orders", "message_key": "{{ $json.id }}" }`,
		Inputs:  "items",
		Outputs: "{success, topic, partition, offset} per item",
	},
	{
		Type:     "queue.rabbitmq",
		Category: "db", // listed under DB: ref nodes prints a fixed category list
		Short:    "Publish a RabbitMQ message per item",
		Description: `Connection: hostname, port, username, password, vhost, ssl/ca/cert/key (or url).
mode queue (queue; declared unless assert_queue=false) or exchange
(exchange, exchange_type fanout|direct|topic|headers, routing_key,
alternate_exchange). durable (persistent messages), auto_delete, exclusive,
arguments, headers, send_input_data / message.`,
		Config:  `{ "credential_id": "my-rabbit", "mode": "exchange", "exchange": "events", "exchange_type": "topic", "routing_key": "user.created" }`,
		Inputs:  "items",
		Outputs: "the input items",
	},
	{
		Type:     "queue.amqp",
		Category: "db", // listed under DB: ref nodes prints a fixed category list
		Short:    "Send an AMQP 1.0 message per item (Artemis, Service Bus, Qpid…)",
		Description: `Connection: hostname, port, username, password, transport_type tcp|tls.
Config: sink (address), header_parameters (application properties),
send_only_property, data_as_object, container_id.`,
		Config:  `{ "credential_id": "my-amqp", "sink": "queue://jobs" }`,
		Inputs:  "items",
		Outputs: "the input items",
	},
	{
		Type:     "queue.mqtt",
		Category: "db", // listed under DB: ref nodes prints a fixed category list
		Short:    "Publish an MQTT message per item",
		Description: `Connection: protocol mqtt|mqtts|ws|wss, host, port, username, password,
client_id, clean, ssl/ca/cert/key. Config: topic, send_input_data / message,
qos 0|1|2, retain.`,
		Config:  `{ "credential_id": "my-broker", "topic": "home/{{ $json.room }}/temp", "qos": 1 }`,
		Inputs:  "items",
		Outputs: "the input items",
	},
}
