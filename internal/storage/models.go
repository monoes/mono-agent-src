package storage

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// NewID generates a new UUID string for use as a primary key.
func NewID() string {
	return uuid.New().String()
}

// Session represents a row in the crawler_sessions table.
type Session struct {
	ID           int       `json:"id"`
	Username     string    `json:"username"`
	Platform     string    `json:"platform"`
	CookiesJSON  string    `json:"cookies_json"`
	Expiry       time.Time `json:"expiry"`
	WhenAdded    time.Time `json:"when_added"`
	ProfilePhoto []byte    `json:"profile_photo,omitempty"`
}

// Person represents a row in the people table.
type Person struct {
	ID               string `json:"id"`
	PlatformUsername string `json:"platform_username"`
	Platform         string `json:"platform"`
	FullName         string `json:"full_name,omitempty"`
	ImageURL         string `json:"image_url,omitempty"`
	ContactDetails   string `json:"contact_details,omitempty"`
	Website          string `json:"website,omitempty"`
	ContentCount     int    `json:"content_count"`
	FollowerCount    string `json:"follower_count,omitempty"`
	FollowingCount   int    `json:"following_count"`
	Introduction     string `json:"introduction,omitempty"`
	IsVerified       bool   `json:"is_verified"`
	Category         string `json:"category,omitempty"`
	JobTitle         string `json:"job_title,omitempty"`
	// Kind is what the row is: "person" (default) or "company" (people.kind,
	// migration 066). Unrelated to Category, the review state.
	Kind string `json:"kind,omitempty"`
	// Profile details a profile read fills (LinkedIn): the tagline, where
	// they are, the About text, and JSON arrays of positions and schools
	// (see data/migrations/053_people_profile_details.sql for their shape).
	Headline   string          `json:"headline,omitempty"`
	Location   string          `json:"location,omitempty"`
	About      string          `json:"about,omitempty"`
	Experience json.RawMessage `json:"experience,omitempty"`
	Education  json.RawMessage `json:"education,omitempty"`
	// ProfileDetails is the JSON object of platform extras a profile read
	// returns (links, pronouns, likes, join date, verification type…; see
	// data/migrations/054_people_profile_extras.sql).
	ProfileDetails json.RawMessage `json:"profile_details,omitempty"`
	ProfileID      string          `json:"profile_id,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// PersonMessage represents a row in the person_messages table: one message
// or interaction ingested from an external source (Outlook, social platforms,
// manual notes, ...) and attached to a person's history.
type PersonMessage struct {
	ID         string `json:"id"`
	PersonID   string `json:"person_id"`
	Source     string `json:"source"`
	ExternalID string `json:"external_id,omitempty"`
	Direction  string `json:"direction"`
	Sender     string `json:"sender,omitempty"`
	Subject    string `json:"subject,omitempty"`
	Body       string `json:"body,omitempty"`
	Metadata   string `json:"metadata,omitempty"`
	// Status: sent (default; every synced message) | draft | failed for outbound
	// compose messages, and the approval lifecycle of a queued send:
	// pending_approval -> approved -> sending -> sent, or rejected / failed /
	// expired (see internal/commsync).
	Status string `json:"status,omitempty"`
	// HilRef is the Human-in-Loop item (hil_pending.id) gating this outbound
	// message; empty for every other message.
	HilRef    string    `json:"hil_ref,omitempty"`
	SentAt    time.Time `json:"sent_at,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// ReadAt is when an inbound message was read; nil = unread. Outbound
	// messages are never unread.
	ReadAt *time.Time `json:"read_at,omitempty"`
}

// PersonStatusUpdate represents a single manually-written status/note entry
// about a person — a personal-CRM-style update (e.g. "Just closed the Q1
// deal"), entirely unrelated to PersonMessage.Status (which tracks
// draft/sent/failed for outbound messages). Append-only: no edit, no delete.
type PersonStatusUpdate struct {
	ID        string    `json:"id"`
	PersonID  string    `json:"person_id"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// SocialList represents a row in the social_lists table.
type SocialList struct {
	ID        string    `json:"id"`
	ListType  string    `json:"list_type,omitempty"`
	Name      string    `json:"name"`
	ItemCount int       `json:"item_count"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SocialListItem represents a row in the social_list_items table.
type SocialListItem struct {
	ID               string    `json:"id"`
	ListID           string    `json:"list_id"`
	Platform         string    `json:"platform"`
	PlatformUsername string    `json:"platform_username"`
	ImageURL         string    `json:"image_url,omitempty"`
	URL              string    `json:"url,omitempty"`
	FullName         string    `json:"full_name,omitempty"`
	ContactDetails   string    `json:"contact_details,omitempty"`
	FollowerCount    int       `json:"follower_count"`
	CreatedAt        time.Time `json:"created_at"`
}

// Template represents a row in the templates table.
type Template struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Subject   string    `json:"subject,omitempty"`
	Body      string    `json:"body"`
	Metadata  string    `json:"metadata,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ConfigEntry represents a row in the configs table.
type ConfigEntry struct {
	Name       string    `json:"name"`
	ConfigData string    `json:"config_data"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}
