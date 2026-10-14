// Package library talks to the monoes.me library: the HTTP API that lists,
// downloads and publishes workflows, orgs, web automations and API
// packages, and the OAuth 2.1 (PKCE loopback) and email-code logins
// MonoAgent uses for it.
//
// Contract: ~/scratch/monoes-library/SPEC.md (monoes.me Library ↔
// MonoAgent integration, v1). The CLI's `library` command group is the only
// caller; the desktop app shells out to it.
package library

import (
	"os"
	"strings"
	"time"
)

// DefaultBaseURL is the library host unless MONOES_BASE_URL says otherwise.
const DefaultBaseURL = "https://monoes.me"

// ClientID is the public OAuth client monoes.me seeds for MonoAgent.
const ClientID = "monoagent"

// Scopes are requested at login: the community scopes MonoAgent already
// used plus the two library ones, and offline_access for a refresh token.
var Scopes = []string{"openid", "profile", "email", "offline_access", "library:read", "library:write"}

// Kinds of library item.
const (
	KindWorkflow   = "workflow"
	KindAutomation = "automation"
	KindOrg        = "org"
	KindAPI        = "api" // a declarative API package (apipkg.v1)
)

// Visibilities.
const (
	VisibilityPrivate  = "private"
	VisibilityPublic   = "public"
	VisibilityOfficial = "official"
)

// OfficialOwner is the username that publishes official items.
const OfficialOwner = "monoes"

// NormalizeKind maps the plural forms monoes.me shows ("automations",
// "workflows", "orgs", "apis") to the kind; anything else is returned as is.
func NormalizeKind(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	switch k {
	case "workflows", "automations", "orgs", "apis":
		return strings.TrimSuffix(k, "s")
	}
	return k
}

// ValidKind reports whether k is a library kind.
func ValidKind(k string) bool {
	return k == KindWorkflow || k == KindAutomation || k == KindOrg || k == KindAPI
}

// BaseURL is MONOES_BASE_URL without a trailing slash, or DefaultBaseURL.
func BaseURL() string {
	if v := strings.TrimSpace(os.Getenv("MONOES_BASE_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return DefaultBaseURL
}

// Owner is an item's owner.
type Owner struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

// Item is one library item as the API returns it.
type Item struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	Slug        string         `json:"slug"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Version     string         `json:"version"`
	Visibility  string         `json:"visibility"`
	Tags        []string       `json:"tags"`
	Owner       Owner          `json:"owner"`
	SHA256      string         `json:"sha256"`
	Size        int64          `json:"size"`
	Meta        map[string]any `json:"meta"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
	URL         string         `json:"url"`
	ArtifactURL string         `json:"artifact_url"`
}

// Official reports whether monoes published the item as official. Both
// the visibility and the owner must say so.
func (it *Item) Official() bool {
	return it.Visibility == VisibilityOfficial && strings.EqualFold(it.Owner.Username, OfficialOwner)
}

// AutomationID is the package id an automation item installs as: the
// meta's automation_id, else the slug.
func (it *Item) AutomationID() string {
	if v, ok := it.Meta["automation_id"].(string); ok && v != "" {
		return v
	}
	return it.Slug
}

// ListResult is GET /api/library/items.
type ListResult struct {
	Items   []Item `json:"items"`
	Page    int    `json:"page"`
	PerPage int    `json:"per_page"`
	Total   int    `json:"total"`
}

// ListQuery filters a list call.
type ListQuery struct {
	Kind    string
	Scope   string // mine | public | official
	Search  string
	Tag     string
	Page    int
	PerPage int
}

// User is the account behind a token.
type User struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Image    string `json:"image"`
}

// Me is GET /api/library/me.
type Me struct {
	User   User     `json:"user"`
	Scopes []string `json:"scopes"`
}

// Token is a stored login.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	Method       string    `json:"method"` // pkce | email
	BaseURL      string    `json:"base_url"`
	User         *User     `json:"user,omitempty"`
}

// expiring reports whether the access token is past (or within skew of)
// its expiry. A token without an expiry never expires locally.
func (t *Token) expiring(now time.Time, skew time.Duration) bool {
	return !t.ExpiresAt.IsZero() && now.Add(skew).After(t.ExpiresAt)
}

// Upload is one publish: the artifact plus its listing fields.
type Upload struct {
	Kind        string
	Visibility  string
	Name        string
	Description string
	Tags        []string
	Version     string
	Filename    string
	ContentType string
	Data        []byte
}
