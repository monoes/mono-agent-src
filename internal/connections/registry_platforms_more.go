package connections

// registryMore holds the communication, database, infrastructure, custom,
// RAG and queue platforms of Registry.
var registryMore = map[string]PlatformDef{
	// ─── Communication ─────────────────────────────────────────────────────────

	"telegram": {
		ID:         "telegram",
		Name:       "Telegram Both",
		Category:   "communication",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{
					Key:      "bot_token",
					Label:    "Bot Token",
					Secret:   true,
					Required: true,
					HelpURL:  "https://core.telegram.org/bots#creating-a-new-bot",
				},
			},
		},
		IconEmoji: "✈️",
	},
	"slack": {
		ID:         "slack",
		Name:       "Slack API",
		Category:   "communication",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodOAuth},
		Fields:     map[AuthMethod][]CredentialField{},
		OAuth: &OAuthConfig{
			AuthURL:      "https://slack.com/oauth/v2/authorize",
			TokenURL:     "https://slack.com/api/oauth.v2.access",
			Scopes:       []string{"channels:read", "chat:write", "users:read"},
			CallbackPort: 9876,
		},
		IconEmoji: "💬",
	},
	"discord": {
		ID:         "discord",
		Name:       "Discord API",
		Category:   "communication",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{
					Key:      "bot_token",
					Label:    "Bot Token",
					Secret:   true,
					Required: true,
					HelpURL:  "https://discord.com/developers/applications",
				},
			},
		},
		IconEmoji: "🎮",
	},
	"twilio": {
		ID:         "twilio",
		Name:       "Twilio API",
		Category:   "communication",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{
					Key:      "account_sid",
					Label:    "Account SID",
					Secret:   false,
					Required: true,
					HelpURL:  "https://console.twilio.com/",
				},
				{Key: "auth_token", Label: "Auth Token", Secret: true, Required: true, HelpURL: "https://console.twilio.com/"},
				{Key: "from_number", Label: "From Number", Secret: false, Required: true, HelpURL: "https://console.twilio.com/"},
			},
		},
		IconEmoji: "📞",
	},
	"whatsapp": {
		ID:         "whatsapp",
		Name:       "WhatsApp API",
		Category:   "communication",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{Key: "account_sid", Label: "Account SID", Secret: false, Required: true},
				{Key: "auth_token", Label: "Auth Token", Secret: true, Required: true},
				{Key: "from_number", Label: "From Number", Secret: false, Required: true},
			},
		},
		IconEmoji: "📱",
	},
	"smtp": {
		ID:         "smtp",
		Name:       "SMTP / Email API",
		Category:   "communication",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodAppPass},
		Fields: map[AuthMethod][]CredentialField{
			MethodAppPass: {
				{Key: "email", Label: "Email", Secret: false, Required: true},
				{Key: "password", Label: "Password", Secret: true, Required: true},
				{Key: "smtp_host", Label: "SMTP Host", Secret: false, Required: true},
				{Key: "smtp_port", Label: "SMTP Port", Secret: false, Required: true},
				{Key: "imap_host", Label: "IMAP Host", Secret: false, Required: false},
				{Key: "imap_port", Label: "IMAP Port", Secret: false, Required: false},
			},
		},
		IconEmoji: "✉️",
	},
	"outlook": {
		ID:         "outlook",
		Name:       "Outlook / Hotmail API",
		Category:   "communication",
		ConnectVia: "API",
		// MethodAppPass drives comm.outlook_send/outlook_read (raw IMAP/SMTP) —
		// Microsoft has deprecated Basic Auth for these on outlook.com/hotmail.com
		// accounts, so this method largely no longer works there; it's kept for
		// any IMAP/SMTP server that still accepts it.
		// MethodOAuth drives service.outlook_mail (Microsoft Graph REST API),
		// which needs no XOAUTH2/IMAP support — it's the working path today.
		Methods: []AuthMethod{MethodOAuth, MethodAppPass},
		Fields: map[AuthMethod][]CredentialField{
			MethodAppPass: {
				{
					Key:      "email",
					Label:    "Email Address",
					Secret:   false,
					Required: true,
					HelpText: "Your Outlook or Hotmail address (e.g. user@outlook.com)",
				},
				{
					Key:      "app_password",
					Label:    "App Password",
					Secret:   true,
					Required: true,
					HelpURL:  "https://account.microsoft.com/security",
					HelpText: "Generate an app password in your Microsoft account security settings",
				},
			},
		},
		OAuth: &OAuthConfig{
			// /common/: supports both personal (outlook.com/hotmail.com) and
			// work/school (Microsoft 365) accounts. Requires the Azure app's
			// "Supported account types" to be set to "Accounts in any
			// organizational directory and personal Microsoft accounts" —
			// Microsoft rejects /common/ with a "userAudience" invalid_request
			// if the app is still registered as "Personal accounts only".
			AuthURL:      "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
			TokenURL:     "https://login.microsoftonline.com/common/oauth2/v2.0/token",
			Scopes:       []string{"https://graph.microsoft.com/Mail.ReadWrite", "https://graph.microsoft.com/Mail.Send", "offline_access"},
			CallbackPort: 9876,
		},
		IconEmoji: "📨",
	},

	// ─── Databases ─────────────────────────────────────────────────────────────

	"postgresql": {
		ID:         "postgresql",
		Name:       "PostgreSQL API",
		Category:   "database",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodConnStr},
		Fields: map[AuthMethod][]CredentialField{
			MethodConnStr: {
				{
					Key:      "connection_string",
					Label:    "Connection String",
					Secret:   true,
					Required: true,
					HelpText: "e.g. postgres://user:password@localhost:5432/dbname",
				},
			},
		},
		IconEmoji: "🐘",
	},
	"mysql": {
		ID:         "mysql",
		Name:       "MySQL API",
		Category:   "database",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodConnStr},
		Fields: map[AuthMethod][]CredentialField{
			MethodConnStr: {
				{
					Key:      "connection_string",
					Label:    "Connection String",
					Secret:   true,
					Required: true,
					HelpText: "e.g. user:password@tcp(localhost:3306)/dbname",
				},
			},
		},
		IconEmoji: "🐬",
	},
	"mongodb": {
		ID:         "mongodb",
		Name:       "MongoDB API",
		Category:   "database",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodConnStr},
		Fields: map[AuthMethod][]CredentialField{
			MethodConnStr: {
				{
					Key:      "connection_string",
					Label:    "Connection String",
					Secret:   true,
					Required: true,
					HelpText: "e.g. mongodb://user:password@localhost:27017/dbname",
				},
			},
		},
		IconEmoji: "🍃",
	},
	"redis": {
		ID:         "redis",
		Name:       "Redis API",
		Category:   "database",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodConnStr},
		Fields: map[AuthMethod][]CredentialField{
			MethodConnStr: {
				{
					Key:      "connection_string",
					Label:    "Connection String",
					Secret:   true,
					Required: true,
					HelpText: "e.g. redis://:password@localhost:6379/0",
				},
			},
		},
		IconEmoji: "🔴",
	},

	// ─── Infrastructure ────────────────────────────────────────────────────────

	"ssh": {
		ID:         "ssh",
		Name:       "SSH",
		Category:   "infrastructure",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodSSHKey, MethodAppPass},
		Fields: map[AuthMethod][]CredentialField{
			MethodSSHKey:  sshKeyFields,
			MethodAppPass: sshPasswordFields,
		},
		IconEmoji: "🔐",
	},
	"sftp": {
		ID:         "sftp",
		Name:       "SFTP",
		Category:   "infrastructure",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodSSHKey, MethodAppPass},
		Fields: map[AuthMethod][]CredentialField{
			MethodSSHKey:  sshKeyFields,
			MethodAppPass: sshPasswordFields,
		},
		IconEmoji: "📂",
	},
	"ftp": {
		ID:         "ftp",
		Name:       "FTP / FTPS",
		Category:   "infrastructure",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodAppPass},
		Fields: map[AuthMethod][]CredentialField{
			MethodAppPass: {
				{Key: "host", Label: "Host", Secret: false, Required: true},
				{Key: "port", Label: "Port", Secret: false, Required: false, HelpText: "Defaults to 21"},
				{Key: "username", Label: "Username", Secret: false, Required: true},
				{Key: "password", Label: "Password", Secret: true, Required: true},
			},
		},
		IconEmoji: "🗄️",
	},

	// ─── Custom ────────────────────────────────────────────────────────────────

	"generic_api": {
		ID:         "generic_api",
		Name:       "Generic API Key",
		Category:   "custom",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{Key: "base_url", Label: "Base URL", Secret: false, Required: true},
				{Key: "api_key", Label: "API Key / Token", Secret: true, Required: true},
				{Key: "header_name", Label: "Header Name", Secret: false, Required: false, HelpText: "Defaults to Authorization"},
			},
		},
		IconEmoji: "🔑",
	},
	"generic_basic": {
		ID:         "generic_basic",
		Name:       "Generic Basic Auth",
		Category:   "custom",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodAppPass},
		Fields: map[AuthMethod][]CredentialField{
			MethodAppPass: {
				{Key: "base_url", Label: "Base URL", Secret: false, Required: true},
				{Key: "username", Label: "Username", Secret: false, Required: true},
				{Key: "password", Label: "Password", Secret: true, Required: true},
			},
		},
		IconEmoji: "🌐",
	},
	"generic_oauth1": {
		ID:         "generic_oauth1",
		Name:       "Generic OAuth 1.0a",
		Category:   "custom",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodOAuth1, MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodOAuth1: {
				{Key: "base_url", Label: "Base URL", HelpText: "Completes the HTTP Request node's path-only URLs"},
				{Key: OAuth1RequestTokenURL, Label: "Request Token URL", Required: true},
				{Key: OAuth1AuthorizationURL, Label: "Authorization URL", Required: true},
				{Key: OAuth1AccessTokenURL, Label: "Access Token URL", Required: true},
				{Key: OAuth1ConsumerKey, Label: "Consumer Key", Required: true},
				{Key: OAuth1ConsumerSecret, Label: "Consumer Secret", Secret: true, Required: true},
				{Key: OAuth1SignatureMethod, Label: "Signature Method", HelpText: "HMAC-SHA1 (default), HMAC-SHA256, HMAC-SHA512 or PLAINTEXT"},
			},
			MethodAPIKey: {
				{Key: "base_url", Label: "Base URL", HelpText: "Completes the HTTP Request node's path-only URLs"},
				{Key: OAuth1ConsumerKey, Label: "Consumer Key", Required: true},
				{Key: OAuth1ConsumerSecret, Label: "Consumer Secret", Secret: true, Required: true},
				{Key: OAuth1Token, Label: "Access Token", Secret: true, Required: true},
				{Key: OAuth1TokenSecret, Label: "Access Token Secret", Secret: true, Required: true},
				{Key: OAuth1SignatureMethod, Label: "Signature Method", HelpText: "HMAC-SHA1 (default), HMAC-SHA256, HMAC-SHA512 or PLAINTEXT"},
			},
		},
		// The endpoints are the connection's own fields.
		OAuth1:    &OAuth1Config{},
		IconEmoji: "🔏",
	},

	// ─── RAG: embedding providers and vector databases (vector.* nodes) ────────

	"openai": apiKeyPlatform("openai", "OpenAI-compatible API", "ai", "🧠",
		CredentialField{Key: "api_key", Label: "API Key", Secret: true, HelpText: "Optional for local servers. OpenAI keys: platform.openai.com/api-keys."},
		CredentialField{Key: "base_url", Label: "Base URL", HelpText: "Defaults to https://api.openai.com/v1. Any OpenAI-compatible server works (OpenRouter, vLLM, LM Studio …)."}),
	"azure_openai": apiKeyPlatform("azure_openai", "Azure OpenAI", "ai", "🧠",
		CredentialField{Key: "api_key", Label: "API Key", Secret: true, Required: true},
		CredentialField{Key: "resource_name", Label: "Resource Name", HelpText: "The <name> in https://<name>.openai.azure.com. Or set Endpoint."},
		CredentialField{Key: "endpoint", Label: "Endpoint", HelpText: "Full endpoint URL; overrides Resource Name."},
		CredentialField{Key: "api_version", Label: "API Version", HelpText: "Defaults to 2024-10-21."}),
	"ollama": apiKeyPlatform("ollama", "Ollama", "ai", "🦙",
		CredentialField{Key: "base_url", Label: "Base URL", Required: true, HelpText: "e.g. http://localhost:11434"},
		CredentialField{Key: "api_key", Label: "API Key", Secret: true, HelpText: "Only for an Ollama behind an authenticating proxy."}),
	"cohere": apiKeyPlatform("cohere", "Cohere", "ai", "🧠",
		CredentialField{Key: "api_key", Label: "API Key", Secret: true, Required: true, HelpText: "dashboard.cohere.com/api-keys"}),
	"mistral": apiKeyPlatform("mistral", "Mistral AI", "ai", "🧠",
		CredentialField{Key: "api_key", Label: "API Key", Secret: true, Required: true, HelpText: "console.mistral.ai/api-keys"}),
	"gemini_api": apiKeyPlatform("gemini_api", "Google Gemini API", "ai", "🧠",
		CredentialField{Key: "api_key", Label: "API Key", Secret: true, Required: true, HelpText: "aistudio.google.com/apikey"}),
	"aws": apiKeyPlatform("aws", "AWS (access keys)", "ai", "☁️",
		CredentialField{Key: "access_key_id", Label: "Access Key ID", Required: true},
		CredentialField{Key: "secret_access_key", Label: "Secret Access Key", Secret: true, Required: true},
		CredentialField{Key: "region", Label: "Region", HelpText: "Defaults to us-east-1."},
		CredentialField{Key: "session_token", Label: "Session Token", Secret: true, HelpText: "Only for temporary credentials."},
		CredentialField{Key: "role_arn", Label: "Role ARN", HelpText: "Optional: assume this IAM role (STS AssumeRole) with the keys above and sign with its temporary credentials."},
		CredentialField{Key: "external_id", Label: "External ID", HelpText: "Optional: the external ID the role's trust policy requires."},
		CredentialField{Key: "role_session_name", Label: "Role Session Name", HelpText: "Optional: defaults to mono-agent."}),
	"qdrant": apiKeyPlatform("qdrant", "Qdrant", "database", "🧭",
		CredentialField{Key: "url", Label: "URL", Required: true, HelpText: "e.g. http://localhost:6333 or your Qdrant Cloud cluster URL"},
		CredentialField{Key: "api_key", Label: "API Key", Secret: true}),
	"pinecone": apiKeyPlatform("pinecone", "Pinecone", "database", "🌲",
		CredentialField{Key: "api_key", Label: "API Key", Secret: true, Required: true, HelpText: "app.pinecone.io → API Keys"}),
	"supabase": apiKeyPlatform("supabase", "Supabase", "database", "⚡",
		CredentialField{Key: "url", Label: "Project URL", Required: true, HelpText: "https://<project>.supabase.co"},
		CredentialField{Key: "api_key", Label: "Service Role Key", Secret: true, Required: true}),
	"weaviate": apiKeyPlatform("weaviate", "Weaviate", "database", "🧭",
		CredentialField{Key: "url", Label: "URL", Required: true, HelpText: "e.g. http://localhost:8080 or your Weaviate Cloud endpoint"},
		CredentialField{Key: "api_key", Label: "API Key", Secret: true}),
	"milvus": apiKeyPlatform("milvus", "Milvus / Zilliz", "database", "🧭",
		CredentialField{Key: "url", Label: "URL", Required: true, HelpText: "e.g. http://localhost:19530"},
		CredentialField{Key: "token", Label: "Token", Secret: true, HelpText: "Zilliz Cloud API key, or user:password"},
		CredentialField{Key: "username", Label: "Username"},
		CredentialField{Key: "password", Label: "Password", Secret: true}),
	"chroma": apiKeyPlatform("chroma", "Chroma", "database", "🧭",
		CredentialField{Key: "url", Label: "URL", Required: true, HelpText: "e.g. http://localhost:8000 or https://api.trychroma.com"},
		CredentialField{Key: "api_key", Label: "API Key / Token", Secret: true},
		CredentialField{Key: "tenant", Label: "Tenant", HelpText: "Defaults to default_tenant."},
		CredentialField{Key: "database", Label: "Database", HelpText: "Defaults to default_database."}),
	"azure_ai_search": apiKeyPlatform("azure_ai_search", "Azure AI Search", "database", "🔎",
		CredentialField{Key: "endpoint", Label: "Endpoint", Required: true, HelpText: "https://<service>.search.windows.net"},
		CredentialField{Key: "api_key", Label: "Admin API Key", Secret: true, Required: true, HelpText: "Search service → Keys"}),

	// ─── More databases, queues and directory (db.* / queue.* nodes) ─────────

	"mssql":       connStrPlatform("mssql", "Microsoft SQL Server", "🗃️", "sqlserver://user:password@host:1433?database=db"),
	"oracle":      connStrPlatform("oracle", "Oracle Database", "🏛️", "oracle://user:password@host:1521/service_name"),
	"timescaledb": connStrPlatform("timescaledb", "TimescaleDB", "🐯", "postgres://user:password@host:5432/db"),
	"cratedb":     connStrPlatform("cratedb", "CrateDB", "📦", "postgres://crate@host:5432/doc"),
	"questdb":     connStrPlatform("questdb", "QuestDB", "📈", "postgres://admin:quest@host:8812/qdb"),
	"snowflake": fieldsPlatform("snowflake", "Snowflake", "database", "❄️",
		CredentialField{Key: "account", Label: "Account Identifier", Required: true, HelpText: "e.g. myorg-myaccount or xy12345.us-east-1"},
		CredentialField{Key: "username", Label: "User", HelpText: "Required for key-pair auth"},
		CredentialField{Key: "private_key", Label: "Private Key (PEM, unencrypted)", Secret: true, HelpText: "Key-pair auth. Or use a token below."},
		CredentialField{Key: "token", Label: "Token", Secret: true, HelpText: "OAuth access token or programmatic access token"},
		CredentialField{Key: "token_type", Label: "Token Type", HelpText: "OAUTH (default) or PROGRAMMATIC_ACCESS_TOKEN"},
		CredentialField{Key: "database", Label: "Database"},
		CredentialField{Key: "schema", Label: "Schema"},
		CredentialField{Key: "warehouse", Label: "Warehouse"},
		CredentialField{Key: "role", Label: "Role"}),
	"ldap": fieldsPlatform("ldap", "LDAP", "infrastructure", "📇",
		CredentialField{Key: "hostname", Label: "Host", Required: true},
		CredentialField{Key: "port", Label: "Port", HelpText: "Defaults to 389 (636 with TLS)"},
		CredentialField{Key: "bind_dn", Label: "Bind DN"},
		CredentialField{Key: "bind_password", Label: "Bind Password", Secret: true},
		CredentialField{Key: "connection_security", Label: "Connection Security", HelpText: "none, tls or startTLS"},
		CredentialField{Key: "ca_certificate", Label: "CA Certificate (PEM)", Secret: true},
		CredentialField{Key: "allow_unauthorized_certs", Label: "Allow Self-Signed Certificates", HelpText: "true / false"}),
	"kafka": fieldsPlatform("kafka", "Kafka", "infrastructure", "🪵", withTLS(
		CredentialField{Key: "brokers", Label: "Brokers", Required: true, HelpText: "Comma-separated host:port list"},
		CredentialField{Key: "client_id", Label: "Client ID"},
		CredentialField{Key: "username", Label: "SASL Username"},
		CredentialField{Key: "password", Label: "SASL Password", Secret: true},
		CredentialField{Key: "sasl_mechanism", Label: "SASL Mechanism", HelpText: "plain, scram-sha-256 or scram-sha-512"})...),
	"rabbitmq": fieldsPlatform("rabbitmq", "RabbitMQ", "infrastructure", "🐇", withTLS(
		CredentialField{Key: "hostname", Label: "Host", Required: true},
		CredentialField{Key: "port", Label: "Port", HelpText: "Defaults to 5672"},
		CredentialField{Key: "username", Label: "Username"},
		CredentialField{Key: "password", Label: "Password", Secret: true},
		CredentialField{Key: "vhost", Label: "Virtual Host", HelpText: "Defaults to /"})...),
	"amqp": fieldsPlatform("amqp", "AMQP 1.0", "infrastructure", "📨",
		CredentialField{Key: "hostname", Label: "Host", Required: true},
		CredentialField{Key: "port", Label: "Port", HelpText: "Defaults to 5672"},
		CredentialField{Key: "username", Label: "Username"},
		CredentialField{Key: "password", Label: "Password", Secret: true},
		CredentialField{Key: "transport_type", Label: "Transport", HelpText: "tcp (default) or tls"}),
	"mqtt": fieldsPlatform("mqtt", "MQTT", "infrastructure", "📡", withTLS(
		CredentialField{Key: "protocol", Label: "Protocol", HelpText: "mqtt (default), mqtts, ws or wss"},
		CredentialField{Key: "host", Label: "Host", Required: true},
		CredentialField{Key: "port", Label: "Port", HelpText: "Defaults to 1883"},
		CredentialField{Key: "username", Label: "Username"},
		CredentialField{Key: "password", Label: "Password", Secret: true},
		CredentialField{Key: "client_id", Label: "Client ID"},
		CredentialField{Key: "clean", Label: "Clean Session", HelpText: "true (default) / false"})...),
}
