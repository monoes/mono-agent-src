// Package apipkg runs declarative API packages ("apipkg.v1"): JSON files
// that describe a REST API's operations, credentials and pagination, so an
// app node is data instead of hand-written Go. One engine executes every
// package; see FORMAT.md for the author guide.
package apipkg

import (
	"bytes"
	"encoding/json"
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

// Key is "<resource>.<operation>", or just the operation when the package
// has no resources.
func (o *Operation) Key() string {
	if o.Resource == "" {
		return o.Operation
	}
	return o.Resource + "." + o.Operation
}

// Pagination types.
const (
	PageCursor = "cursor"
	// PageHeaderCursor: the cursor comes from a response header
	// (cursor_path = header name) and goes back as a request header
	// (cursor_param = header name): Cosmos DB x-ms-continuation.
	PageHeaderCursor = "header_cursor"
	PageOffset       = "offset"
	PagePage         = "page"
	PageLinkHeader   = "link_header"
	PageNextURL      = "next_url"
)

// Pagination describes how a list operation pages.
type Pagination struct {
	Type        string `json:"type"`
	CursorParam string `json:"cursor_param,omitempty"`
	CursorPath  string `json:"cursor_path,omitempty"`
	HasMorePath string `json:"has_more_path,omitempty"`
	OffsetParam string `json:"offset_param,omitempty"`
	LimitParam  string `json:"limit_param,omitempty"`
	PageParam   string `json:"page_param,omitempty"`
	NextURLPath string `json:"next_url_path,omitempty"`
	// NextURLPrefix (a template) is prepended to a relative next URL whose
	// base is not the request host (Confluence v2 _links.next).
	NextURLPrefix string `json:"next_url_prefix,omitempty"`
	// Values: the page's results are an object keyed by id (Mautic
	// {"contacts": {"12": {…}}}); its values are the list (numeric keys in
	// numeric order, else sorted).
	Values bool `json:"values,omitempty"`
	// When (a template) turns paging off when it renders false: one plain
	// request, no page params (Strapi v3 vs v4 by credential). The param
	// names (cursor/offset/limit/page_param) may be templates too.
	When           string `json:"when,omitempty"`
	ResultsPath    string `json:"results_path,omitempty"`
	ReturnAllField string `json:"return_all_field,omitempty"`
	LimitField     string `json:"limit_field,omitempty"`

	// PageSize is the page size requested with limit_param (default 100).
	PageSize int `json:"page_size,omitempty"`
	// StartPage is the first page number for type "page" (default 1).
	StartPage *int `json:"start_page,omitempty"`
	// ParamIn is where page params go: "query" (default) or "body" (a JSON
	// body, e.g. search endpoints that take the cursor in the body).
	ParamIn string `json:"param_in,omitempty"`
	// MaxPages caps one run (default 1000); items get
	// pagination_truncated=true when it stops a list early.
	MaxPages int `json:"max_pages,omitempty"`
	// Next sends every page after the first as this request instead of
	// the op's (continuation endpoints: Dropbox list_folder/continue with
	// only the cursor; Elasticsearch search_after with the new PIT id).
	// res is the previous page and page.cursor its cursor.
	Next *Step `json:"next,omitempty"`
}

// Output says which part of the response becomes the node's items.
type Output struct {
	Path  string `json:"path,omitempty"`
	Split *bool  `json:"split,omitempty"`
	// HeaderRow turns a list of value rows into objects keyed by the
	// first row (Google Sheets values, CSV-like APIs).
	HeaderRow bool `json:"header_row,omitempty"`
	// Each replaces every result element by its value at this dot path
	// (Chargebee lists return [{"invoice": {…}}]; n8n emits the invoice).
	Each string `json:"each,omitempty"`
	// Match keeps only the result elements that satisfy n8n-style filter
	// rows (Google Sheets read lookups).
	Match *OutputMatch `json:"match,omitempty"`
	// Expand sends one request per result element (el = the element) and
	// replaces the element with the decoded response (Gmail lists ids,
	// then GETs each message).
	Expand *Step `json:"expand,omitempty"`
	// ExpandMerge: with expand, set element[key] to the response's value at
	// path ("" = the whole response) instead of replacing the element
	// (Gong call transcripts).
	ExpandMergeKey  string `json:"expand_merge_key,omitempty"`
	ExpandMergePath string `json:"expand_merge_path,omitempty"`
	// From (a template over res) replaces the decoded response before
	// path/split: "{{ res | values }}" for maps keyed by id.
	From string `json:"from,omitempty"`
	// Merge folds a list of objects into one object (Hue update results).
	Merge bool `json:"merge,omitempty"`
	// Skip drops the first n elements (NextCloud lists the folder itself
	// first); Sort orders elements by a dot path (SortDesc: descending).
	Skip     int    `json:"skip,omitempty"`
	Sort     string `json:"sort,omitempty"`
	SortDesc bool   `json:"sort_desc,omitempty"`
	// Limit applies n8n's returnAll/limit fields to a list that isn't
	// paginated (client-side slicing).
	Limit bool `json:"limit,omitempty"`
	// To reshapes each result element with a template (el = the element),
	// e.g. {"id": "{{ el.id }}", "$merge": "{{ el.values }}"}. ToWhen, a
	// template, skips the reshape when it renders empty/false (rawData).
	To     any    `json:"to,omitempty"`
	ToWhen string `json:"to_when,omitempty"`
	// MatchAfterTo runs Match on the elements reshaped by To instead of
	// the raw ones (Excel rows zipped with their column names, then
	// filtered by column).
	MatchAfterTo bool `json:"match_after_to,omitempty"`
	// Firestore (a template): when true, decode Firestore documents
	// ({name, fields{typed values}}) into plain JSON with _name, _id,
	// _createTime and _updateTime (n8n's "simple" output).
	Firestore string `json:"firestore,omitempty"`
	// Download fetches files per element onto the item's binary data.
	Download *DownloadStep `json:"download,omitempty"`
	// Where (a template, el = the element) drops the elements it renders
	// empty or false for (Graph delta: only entries with a file facet).
	Where string `json:"where,omitempty"`
	// Walk appends the records of a tree walk (a request repeated over a
	// growing frontier of ids) to the main result: Confluence descendants.
	Walk *Walk `json:"walk,omitempty"`
	// SchemaRows is a path (in the whole response) to a BigQuery-style
	// schema ([{name, type, mode, fields}]); the rows at output.path
	// ({f: [{v}]}) become objects keyed by field name. Numbers (a
	// template) casts INTEGER/NUMERIC/FLOAT values to numbers when true.
	SchemaRows string `json:"schema_rows,omitempty"`
	Numbers    string `json:"numbers,omitempty"`
}

// OutputMatch filters result elements by condition rows. Rows is a
// template resolving to a list of objects; each row's Column/Value keys
// name the element field and the wanted value. Combine ("AND"/"OR",
// default AND) and First (keep only the first match) are templates.
type OutputMatch struct {
	Rows    string `json:"rows"`
	Column  string `json:"column"`
	Value   string `json:"value"`
	Combine string `json:"combine,omitempty"`
	First   string `json:"first,omitempty"`
}

// Binary configures file upload and download.
type Binary struct {
	// Download: the response body is a file, stored on the output item's
	// binary data.
	Download bool `json:"download,omitempty"`
	// UploadField is the op param naming the input item's binary property
	// to send (n8n's "binaryPropertyName").
	UploadField string `json:"upload_field,omitempty"`
	// PropertyField is the op param naming the output binary property for a
	// download (default property "data"). UploadField and PropertyField may
	// be dotted ("options.binaryPropertyOutput") to read a collection key.
	PropertyField string `json:"property_field,omitempty"`
	// MultipartField is the form field of the file part (default "file").
	MultipartField string `json:"multipart_field,omitempty"`
	// UploadWhen (a template) decides per run whether to upload at all
	// (n8n sendBinaryData); when false the op sends its body (JSON, or
	// none). Property (a template) overrides the binary property read
	// from upload_field ("formField:binaryProperty" values).
	UploadWhen string `json:"upload_when,omitempty"`
	// FallbackBodyType is the body type when upload_when turns the upload
	// off (default: json when the op has a body, else none).
	FallbackBodyType string `json:"fallback_body_type,omitempty"`
	Property         string `json:"property,omitempty"`
	// Properties (a template giving a list or comma text) uploads several
	// binary properties: one multipart part each (el = {property, index}
	// in multipart_field), and bin.files lists them for JSON bodies.
	Properties string `json:"properties,omitempty"`
	// FileName and MimeType are templates for the uploaded part.
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	// DownloadWhen (a template) decides per run whether the response is a
	// file (GCS alt=media, Slides thumbnail download); it overrides Download.
	DownloadWhen string `json:"download_when,omitempty"`
	// UploadText (a template): when it renders non-empty, its text is the
	// upload instead of the input item's binary (GCS create from text).
	UploadText string `json:"upload_text,omitempty"`
	// UploadURL (a template): when it renders a URL, the file at that URL
	// is fetched (GET, without the credential) and uploaded in place of
	// the input item's binary (Airtop's "upload from URL" source).
	UploadURL string `json:"upload_url,omitempty"`
	// UploadSession sends files larger than MinSize in chunks through a
	// resumable upload session (Microsoft Graph createUploadSession)
	// instead of one raw request.
	UploadSession *UploadSession `json:"upload_session,omitempty"`
	// DownloadBase64 (a template over res): when the response carries the
	// file base64-encoded in JSON (GitHub contents), decode it onto the
	// item's binary and keep the JSON response as the item's json.
	DownloadBase64 string `json:"download_base64,omitempty"`
	// DownloadEach is a response path to a list of base64 files: each
	// element (el in download_base64, file_name, mime_type) becomes one
	// item (OpenAI image generations with n > 1).
	DownloadEach string `json:"download_each,omitempty"`
}

// NodeTypeOf is the node type a package registers ("" for extends).
func (p *Package) NodeTypeOf() string {
	if p.Extends != "" {
		return ""
	}
	if p.NodeType != "" {
		return p.NodeType
	}
	return NodeTypePrefix + p.ID
}

// TargetType is the node type whose config runs this package: its own node
// type, or the native node it extends.
func (p *Package) TargetType() string {
	if p.Extends != "" {
		return p.Extends
	}
	return p.NodeTypeOf()
}

// Op returns the operation for (resource, operation).
func (p *Package) Op(resource, operation string) (*Operation, bool) {
	for i := range p.Operations {
		o := &p.Operations[i]
		if o.Resource == resource && o.Operation == operation {
			return o, true
		}
	}
	return nil, false
}

// DefaultResource is the resource used when a config names none: the first
// resource ("" for a package without resources).
func (p *Package) DefaultResource() string {
	if len(p.Resources) == 0 {
		return ""
	}
	return p.Resources[0].Value
}

// DefaultOperation is the first operation listed for resource.
func (p *Package) DefaultOperation(resource string) string {
	for _, o := range p.Operations {
		if o.Resource == resource {
			return o.Operation
		}
	}
	return ""
}

// PrimaryPlatform is the connection platform of the first credential, or "".
func (p *Package) PrimaryPlatform() string {
	for _, c := range p.Credentials {
		if pl := c.EffectivePlatform(); pl != "" {
			return pl
		}
	}
	return ""
}

// EffectivePlatform is reuse_platform when set, else platform.
func (c *Credential) EffectivePlatform() string {
	if c.ReusePlatform != "" {
		return c.ReusePlatform
	}
	return c.Platform
}

// hasMethod reports whether the credential offers method m.
func (c *Credential) hasMethod(m string) bool {
	for _, x := range c.Methods {
		if x == m {
			return true
		}
	}
	return false
}

// Steps is one Step or a list of them in JSON.
type Steps []Step

// UnmarshalJSON accepts a single step object or a list of steps.
func (s *Steps) UnmarshalJSON(b []byte) error {
	t := bytes.TrimSpace(b)
	if len(t) > 0 && t[0] == '{' {
		var one Step
		if err := json.Unmarshal(t, &one); err != nil {
			return err
		}
		*s = Steps{one}
		return nil
	}
	var many []Step
	if err := json.Unmarshal(t, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

// Poll is a request repeated until a condition holds.
type Poll struct {
	Step
	Until      string `json:"until"`
	IntervalMs int    `json:"interval_ms,omitempty"` // default 1000
	MaxWaitMs  int    `json:"max_wait_ms,omitempty"` // default 300000
	// MaxWaitSeconds (a template) overrides MaxWaitMs when it renders a
	// number: the wait comes from a param (n8n's timeout fields).
	MaxWaitSeconds string `json:"max_wait_seconds,omitempty"`
}

// Repeat is a step's wait condition: the step is resent every IntervalMs
// (default 1000) with res = the latest response until Until renders true,
// failing after the max wait (default 5 minutes).
type Repeat struct {
	Until          string `json:"until"`
	IntervalMs     int    `json:"interval_ms,omitempty"`
	MaxWaitMs      int    `json:"max_wait_ms,omitempty"`
	MaxWaitSeconds string `json:"max_wait_seconds,omitempty"`
}

// Stream reads a text/event-stream response event by event. Each event's
// data (JSON-decoded when it parses) is res for Until; the first event for
// which Until renders true becomes the response. The request fails when
// the stream ends first or after the timeout (default 5 minutes).
type Stream struct {
	Until          string `json:"until"`
	TimeoutMs      int    `json:"timeout_ms,omitempty"`
	TimeoutSeconds string `json:"timeout_seconds,omitempty"`
}

// HMACInject is a per-request HMAC signature header.
type HMACInject struct {
	Header   string `json:"header"`
	Secret   string `json:"secret"`
	Message  string `json:"message"`
	Alg      string `json:"alg,omitempty"`      // SHA256 (default), SHA1, SHA512
	Encoding string `json:"encoding,omitempty"` // base64 (default) or hex
	Prefix   string `json:"prefix,omitempty"`
}

// AzureSharedKeyInject: Account and Key (base64) are templates.
type AzureSharedKeyInject struct {
	Account string `json:"account"`
	Key     string `json:"key"`
}

// AzureCosmosInject: Key (base64 master key) is a template.
type AzureCosmosInject struct {
	Key string `json:"key"`
}
