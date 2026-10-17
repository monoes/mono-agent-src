// Package apipkg runs declarative API packages ("apipkg.v1"): JSON files
// that describe a REST API's operations, credentials and pagination, so an
// app node is data instead of hand-written Go. One engine executes every
// package; see FORMAT.md for the author guide.
package apipkg

import (
	"github.com/monoes/mono-agent/internal/workflow"
)

// SchemaID is the value of "$schema" in every package file.
const SchemaID = "apipkg.v1"

// NodeTypePrefix is the namespace of the node types packages register.
const NodeTypePrefix = "app."

// Package is one API package file.
type Package struct {
	Schema      string            `json:"$schema"`
	ID          string            `json:"id"`
	NodeType    string            `json:"node_type,omitempty"`
	Extends     string            `json:"extends,omitempty"`
	Title       string            `json:"title"`
	Description string            `json:"description,omitempty"`
	Category    string            `json:"category,omitempty"`
	DocsURL     string            `json:"docs_url,omitempty"`
	N8nTypes    []string          `json:"n8n_types,omitempty"`
	BaseURL     string            `json:"base_url"`
	Headers     map[string]string `json:"headers,omitempty"`
	Credentials []Credential      `json:"credentials,omitempty"`
	Resources   []Resource        `json:"resources,omitempty"`
	Operations  []Operation       `json:"operations"`

	// N8nTriggerTypes are n8n trigger node types of the same app. Triggers
	// are not supported yet; the importer reports them as such.
	N8nTriggerTypes []string `json:"n8n_trigger_types,omitempty"`
	// N8nOperationParam names the n8n param that selects the operation
	// when it isn't "operation" (FileMaker: "action"). A config without
	// "operation" uses that param's value, so imported nodes run as is.
	N8nOperationParam string `json:"n8n_operation_param,omitempty"`
	// N8nResourceParam names the n8n param that selects the resource when
	// it isn't "resource" (uProc: "group").
	N8nResourceParam string `json:"n8n_resource_param,omitempty"`
	// N8nVersionDefaults: per n8n node typeVersion, param defaults the
	// importer fills in when an exported node omits them (n8n leaves
	// defaults out; Invoice Ninja v1 meant apiVersion v4).
	N8nVersionDefaults map[string]map[string]any `json:"n8n_version_defaults,omitempty"`
	// QueryStyle "brackets" encodes nested query/form values PHP-style
	// (a[b][0][c]=v); an op's own query_style overrides it.
	QueryStyle string `json:"query_style,omitempty"`
	// ErrorWhen (template over res, hdr, cred) fails a 2xx response
	// that carries an error (JSON-RPC "error", ok:false); ErrorMessage
	// is the error's text. An operation's own pair replaces these.
	ErrorWhen    string `json:"error_when,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	// Webhook registers the app's webhook trigger with the vendor on
	// activation (see webhook.go).
	Webhook *Webhook `json:"webhook,omitempty"`
	// Outputs are the keys of every op's items, for ops that declare
	// none (see Operation.Outputs).
	Outputs []workflow.NodeSchemaOutput `json:"outputs,omitempty"`

	// Source is where the package was loaded from ("catalog" or a file
	// path). Not part of the file.
	Source string `json:"-"`
}

// Resource is one n8n "resource" option.
type Resource struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Credential describes how a package authenticates.
type Credential struct {
	Platform      string            `json:"platform"`
	Name          string            `json:"name,omitempty"`
	N8nCredential string            `json:"n8n_credential,omitempty"`
	Methods       []string          `json:"methods"`
	Fields        []CredentialField `json:"fields,omitempty"`
	OAuth         *OAuthSpec        `json:"oauth,omitempty"`
	Inject        Inject            `json:"inject"`
	ReusePlatform string            `json:"reuse_platform,omitempty"`
	// ScopeGroup names the umbrella-connection scope group the package
	// needs (google: calendar, docs, forms, …; microsoft: mail, files, …).
	// Before the first request the engine checks the connection grants it
	// and fails with the exact `connect … --scopes` command when not.
	ScopeGroup string `json:"scope_group,omitempty"`

	// N8nCredentialAlt are further n8n credential types that map to this
	// credential (e.g. the OAuth2 variant of an API-key credential).
	N8nCredentialAlt []string `json:"n8n_credential_alt,omitempty"`
	// N8nFieldMap renames n8n credential fields to ours (n8n → ours).
	N8nFieldMap map[string]string `json:"n8n_field_map,omitempty"`
}

// CredentialField is one input of a credential form.
type CredentialField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Secret   bool   `json:"secret,omitempty"`
	Required bool   `json:"required,omitempty"`
	Help     string `json:"help,omitempty"`
}

// OAuthSpec configures an OAuth 2.0 authorization-code flow (always PKCE).
type OAuthSpec struct {
	AuthURL     string            `json:"auth_url,omitempty"`
	TokenURL    string            `json:"token_url"`
	Scopes      []string          `json:"scopes,omitempty"`
	PKCE        bool              `json:"pkce,omitempty"`
	ExtraParams map[string]string `json:"extra_params,omitempty"`
	// Client-credentials grant (method "client_credentials"): which
	// credential fields hold the client id/secret (default client_id /
	// client_secret) and how they are sent: "body" (default) or "basic".
	// token_url and extra_params values are templates over cred.*.
	// TokenFields are extra token-response keys stored on the connection
	// (GoTo Webinar organizer_key/account_key) and readable as cred.<key>.
	TokenFields       []string `json:"token_fields,omitempty"`
	ClientIDField     string   `json:"client_id_field,omitempty"`
	ClientSecretField string   `json:"client_secret_field,omitempty"`
	ClientAuth        string   `json:"client_auth,omitempty"`
	// ConnectFields are credential field keys asked before the browser
	// flow and stored on the connection; auth_url, token_url, scopes and
	// extra_params may then be templates over them
	// ("https://{{ cred.subdomain }}.zendesk.com/oauth/tokens").
	ConnectFields []string `json:"connect_fields,omitempty"`
	// Metadata is a request sent with a new token, whose response values
	// are stored on the connection (Mailchimp: the data centre). It also
	// runs before a node's first request when those keys are missing.
	Metadata *MetadataSpec `json:"metadata,omitempty"`
	// OAuth 1.0a (method "oauth1"): the request-token endpoint and the
	// signature method (default HMAC-SHA1); auth_url is the authorization
	// page and token_url the access-token endpoint.
	RequestTokenURL string `json:"request_token_url,omitempty"`
	SignatureMethod string `json:"signature_method,omitempty"`
}

// MetadataSpec fetches account details with an OAuth token. URL and
// header values are templates over cred.* (access_token included); Fields
// maps a connection key to a path in the JSON response.
type MetadataSpec struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Fields  map[string]string `json:"fields"`
}

// Inject says how a credential authenticates a request. Every value is a
// template over cred.*.
type Inject struct {
	Headers  map[string]string `json:"headers,omitempty"`
	Query    map[string]string `json:"query,omitempty"`
	Basic    *BasicInject      `json:"basic,omitempty"`
	AWSSigV4 *AWSSigV4Inject   `json:"aws_sigv4,omitempty"`
	JWT      *JWTInject        `json:"jwt,omitempty"`
	// Body is merged into JSON/form/multipart bodies (Mandrill's "key").
	Body map[string]string `json:"body,omitempty"`
	// HMAC signs each request: header = prefix + encode(HMAC(secret,
	// message)); message is a template over req (method, path, query,
	// body) — Unleashed signs the encoded query string.
	HMAC *HMACInject `json:"hmac,omitempty"`
	// AzureSharedKey signs Azure Storage requests (SharedKey).
	AzureSharedKey *AzureSharedKeyInject `json:"azure_shared_key,omitempty"`
	// AzureCosmos signs Cosmos DB requests with the master key.
	AzureCosmos *AzureCosmosInject `json:"azure_cosmos,omitempty"`
	// OAuth1 signs each request with OAuth 1.0a (Trello, Twitter v1.1).
	OAuth1 *OAuth1Inject `json:"oauth1,omitempty"`
}

// OAuth1Inject signs requests with OAuth 1.0a. The keys are templates and
// default to cred.consumer_key, cred.consumer_secret, cred.oauth_token and
// cred.oauth_token_secret; signature_method defaults to the credential's
// oauth.signature_method, else HMAC-SHA1.
type OAuth1Inject struct {
	ConsumerKey     string `json:"consumer_key,omitempty"`
	ConsumerSecret  string `json:"consumer_secret,omitempty"`
	Token           string `json:"token,omitempty"`
	TokenSecret     string `json:"token_secret,omitempty"`
	SignatureMethod string `json:"signature_method,omitempty"`
	Realm           string `json:"realm,omitempty"`
}

// JWTInject signs a short-lived JWT per request (Ghost Admin API, Zoom
// JWT apps, App Store Connect). Secret and Kid are templates; the claims
// are templates too; iat and exp (iat + ttl_s, default 300) are added.
type JWTInject struct {
	Header         string         `json:"header,omitempty"` // default Authorization
	Prefix         string         `json:"prefix,omitempty"` // default "Bearer "
	Alg            string         `json:"alg"`              // HS256/384/512, RS256, ES256
	Secret         string         `json:"secret"`           // HMAC secret or PEM private key
	SecretEncoding string         `json:"secret_encoding,omitempty"`
	Kid            string         `json:"kid,omitempty"`
	Claims         map[string]any `json:"claims,omitempty"`
	TTLs           int            `json:"ttl_s,omitempty"`
}

// BasicInject is HTTP basic authentication.
type BasicInject struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// AWSSigV4Inject signs requests with AWS Signature Version 4. Keys come from
// cred.access_key_id, cred.secret_access_key and cred.session_token unless
// overridden.
type AWSSigV4Inject struct {
	Service         string `json:"service"`
	Region          string `json:"region"`
	AccessKeyID     string `json:"access_key_id,omitempty"`
	SecretAccessKey string `json:"secret_access_key,omitempty"`
	SessionToken    string `json:"session_token,omitempty"`
}

// Operation is one (resource, operation) pair and the request it makes.
type Operation struct {
	Resource    string                     `json:"resource"`
	Operation   string                     `json:"operation"`
	Title       string                     `json:"title"`
	Description string                     `json:"description,omitempty"`
	Method      string                     `json:"method"`
	Path        string                     `json:"path"`
	BaseURL     string                     `json:"base_url,omitempty"`
	Query       map[string]any             `json:"query,omitempty"`
	Body        any                        `json:"body,omitempty"`
	BodyType    string                     `json:"body_type,omitempty"`
	Headers     map[string]string          `json:"headers,omitempty"`
	Fields      []workflow.NodeSchemaField `json:"fields,omitempty"`
	Pagination  *Pagination                `json:"pagination,omitempty"`
	Output      *Output                    `json:"output,omitempty"`
	Binary      *Binary                    `json:"binary,omitempty"`
	N8nParamMap map[string]string          `json:"n8n_param_map,omitempty"`
	QueryStyle  string                     `json:"query_style,omitempty"`
	// Also are follow-up requests sent after the main one, in order, for
	// n8n operations that make several calls (e.g. update fields, then
	// tags). Each may read res (the main response). The node's output is
	// still the main response.
	Also []Step `json:"also,omitempty"`
	// Batch sends ONE request for all input items (n8n batchCreate/
	// batchUpdate/batchDelete): params come from the first item and the
	// items root holds every item's JSON.
	Batch bool `json:"batch,omitempty"`
	// ScopeGroup overrides the credential's scope_group for this op: an
	// umbrella connection must grant this group instead (Outlook calendar
	// and contact ops need "calendar"/"contacts", not "mail").
	ScopeGroup string `json:"scope_group,omitempty"`
	// Poll repeats a request after the main one until Until (a template
	// over res = the latest response) renders true (BigQuery jobs that
	// are not complete yet). The last response replaces the main one.
	Poll *Poll `json:"poll,omitempty"`
	// Stream reads the main response as server-sent events until one
	// matches; that event is the op's response (Airtop session events).
	Stream *Stream `json:"stream,omitempty"`
	// Before is one request, or a list run in order, sent first. pre is
	// the last decoded response and pres.<i> each step's; later steps see
	// earlier ones (e.g. resolve a sheet title, then read its header row).
	Before Steps `json:"before,omitempty"`
	// ErrorWhen / ErrorMessage replace the package's for this op.
	ErrorWhen    string `json:"error_when,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	// Bulk, when its when template is true for the first item, sends the
	// op for all input items as one request per chunk (n8n's "bulk
	// operation" option: Elasticsearch _bulk) instead of one per item.
	Bulk *Bulk `json:"bulk,omitempty"`
	// Outputs are the keys of the op's items as the node schema lists
	// them. Unset, they are derived from binary and output (outputs.go).
	Outputs []workflow.NodeSchemaOutput `json:"outputs,omitempty"`
	// Wait makes the op n8n's dispatch-and-wait (wait.go): the request is
	// sent for the first item with hook.url bound to the execution's resume
	// URL, then the node pauses until that URL is called.
	Wait *Wait `json:"wait,omitempty"`
	// Sends marks an op that sends a message to people or posts in public
	// (sends.go); "sends": false, with SendsReason, one that only looks like
	// it does.
	Sends       *Sends `json:"sends,omitempty"`
	SendsReason string `json:"sends_reason,omitempty"`
	// Subfields declares the keys of an option whose value is an object (or
	// a list of them), keyed "collection.option": only those pass into the
	// request (collections.go). An option without an entry is not checked.
	Subfields map[string][]string `json:"subfields,omitempty"`
}

// Wait is how a dispatch-and-wait op waits for its callback.
type Wait struct {
	// Method is the method the callback must use ("" = any).
	Method string `json:"method,omitempty"`
	// Limit is the dotted param path of n8n's wait limit ({limitType,
	// resumeAmount, resumeUnit, maxDateAndTime}, e.g.
	// "options.limitWaitTime.values"); unset or absent waits until called.
	Limit string `json:"limit,omitempty"`
}

// Bulk is an operation's one-request-for-many-items variant. The batch
// root lists every item of the chunk as {p, item, index} (its params,
// its JSON, its position); items is their JSON as for batch ops; p is
// the chunk's first item's params. Output and error_when shape the bulk
// response instead of the op's.
type Bulk struct {
	When string `json:"when"`
	// Size is the number of items per request (default: all in one).
	Size         int               `json:"size,omitempty"`
	Method       string            `json:"method"`
	Path         string            `json:"path"`
	BaseURL      string            `json:"base_url,omitempty"`
	Query        map[string]any    `json:"query,omitempty"`
	Body         any               `json:"body,omitempty"`
	BodyType     string            `json:"body_type,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	Output       *Output           `json:"output,omitempty"`
	ErrorWhen    string            `json:"error_when,omitempty"`
	ErrorMessage string            `json:"error_message,omitempty"`
}

// Step is one follow-up request of an operation. When is a template; an
// empty result skips the step (e.g. "{{ p.updateFields.tags }}").
type Step struct {
	When string `json:"when,omitempty"`
	// ForEach (also steps): a template resolving to a list; the step is
	// sent once per element with el bound (Egoi: one call per tag id).
	ForEach string `json:"for_each,omitempty"`
	// download marks an output.download request: the response is a file
	// (no Accept: application/json).
	download bool
	// Upload sends the op's binary (binary.upload_field/property) as the
	// raw body, Content-Type = its MIME type (register-then-upload APIs).
	Upload bool `json:"upload,omitempty"`
	// Auth false sends the step without the credential (presigned S3/GCS/
	// Azure SAS URLs reject a second auth mechanism).
	Auth     *bool             `json:"auth,omitempty"`
	Method   string            `json:"method"`
	Path     string            `json:"path"`
	BaseURL  string            `json:"base_url,omitempty"`
	Query    map[string]any    `json:"query,omitempty"`
	Body     any               `json:"body,omitempty"`
	BodyType string            `json:"body_type,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	// ErrorWhen / ErrorMessage replace the op's (and package's) 2xx error
	// check for this step's responses: a poll whose pending reply says
	// success:false fails only on a real error (Dropcontact).
	ErrorWhen    string `json:"error_when,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	// Repeat resends the step until its until template holds (a before
	// step that waits for an uploaded file to become available).
	Repeat *Repeat `json:"repeat,omitempty"`
	// Stream reads the response as server-sent events until one matches
	// (see Stream).
	Stream *Stream `json:"stream,omitempty"`
}
