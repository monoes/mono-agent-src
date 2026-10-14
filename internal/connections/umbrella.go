package connections

// Umbrella platforms: one OAuth connection per provider account ("google",
// "microsoft") that covers every product of that provider, with the scope
// set chosen at connect time. The older per-product platforms (gmail,
// google_sheets, google_drive, youtube, outlook) stay registered and keep
// working; a node whose credential_platform is one of them also accepts an
// umbrella connection (see CompatiblePlatforms and Store.GetOrResolve).

// ScopeGroup is a named, user-facing bundle of OAuth scopes — what
// `monoagentcli connect google --scopes sheets,gmail` selects.
type ScopeGroup struct {
	Name        string
	Description string
	Scopes      []string
	// OptIn leaves the group out of a plain `connect` (no --scopes): it is
	// granted only when named, or with "all". Rarely used APIs whose scopes
	// Google treats as sensitive, and Microsoft scopes that need a tenant
	// admin's consent, are opt-in, so a default connect does not ask for them.
	OptIn bool
}

// umbrellaDef describes one umbrella platform's scope catalogue.
type umbrellaDef struct {
	// Always lists scopes requested on every connect (account identity,
	// refresh tokens) regardless of the selected groups.
	Always []string
	// Groups is the ordered scope-group catalogue.
	Groups []ScopeGroup
	// Members are the legacy per-product platforms this umbrella stands in
	// for, mapped to the scope group each one needs. Their stored OAuth
	// clients and env vars are the fallback client-id sources, in this order.
	Members []umbrellaMember
}

type umbrellaMember struct {
	Platform string
	Group    string
}

const (
	googleScopePrefix    = "https://www.googleapis.com/auth/"
	microsoftScopePrefix = "https://graph.microsoft.com/"
)

func gScopes(names ...string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = googleScopePrefix + n
	}
	return out
}

func msScopes(names ...string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = microsoftScopePrefix + n
	}
	return out
}

var umbrellas = map[string]umbrellaDef{
	"google": {
		Always: append([]string{"openid"}, gScopes("userinfo.email")...),
		Groups: []ScopeGroup{
			{Name: "sheets", Description: "Google Sheets (read/write spreadsheets, list them via Drive)", Scopes: gScopes("spreadsheets", "drive.readonly")},
			{Name: "gmail", Description: "Gmail (read, send, label, trash)", Scopes: gScopes("gmail.modify")},
			{Name: "drive", Description: "Google Drive (full file access)", Scopes: gScopes("drive")},
			{Name: "youtube", Description: "YouTube (upload, manage, comment)", Scopes: gScopes("youtube.upload", "youtube.force-ssl")},
			{Name: "calendar", Description: "Google Calendar events", Scopes: gScopes("calendar.events")},
			{Name: "docs", Description: "Google Docs", Scopes: gScopes("documents")},
			{Name: "forms", Description: "Google Forms (read forms and responses)", Scopes: gScopes("forms.body.readonly", "forms.responses.readonly")},
			{Name: "tasks", Description: "Google Tasks", Scopes: gScopes("tasks")},
			{Name: "contacts", Description: "Google Contacts (read/write)", Scopes: gScopes("contacts")},
			// Opt-in: requested only when named in --scopes (or "all").
			{Name: "translate", Description: "Cloud Translation", Scopes: gScopes("cloud-translation"), OptIn: true},
			{Name: "naturallanguage", Description: "Cloud Natural Language", Scopes: gScopes("cloud-language"), OptIn: true},
			{Name: "chat", Description: "Google Chat (spaces and messages)", Scopes: gScopes("chat.messages", "chat.spaces"), OptIn: true},
			{Name: "bigquery", Description: "BigQuery", Scopes: gScopes("bigquery"), OptIn: true},
			{Name: "cloudstorage", Description: "Cloud Storage (read/write buckets and objects)", Scopes: gScopes("devstorage.read_write"), OptIn: true},
			{Name: "firebase", Description: "Firebase (Cloud Firestore and Realtime Database)", Scopes: gScopes("datastore", "firebase.database", "userinfo.email"), OptIn: true},
			{Name: "ads", Description: "Google Ads", Scopes: gScopes("adwords"), OptIn: true},
			{Name: "analytics", Description: "Google Analytics (read reports)", Scopes: gScopes("analytics.readonly"), OptIn: true},
			{Name: "books", Description: "Google Books (bookshelves)", Scopes: gScopes("books"), OptIn: true},
			{Name: "businessprofile", Description: "Google Business Profile (locations, posts, reviews)", Scopes: gScopes("business.manage"), OptIn: true},
			{Name: "formsedit", Description: "Google Forms editing (create forms, add and change questions)", Scopes: gScopes("forms.body"), OptIn: true},
			{Name: "admin", Description: "Workspace Admin directory (users, groups, ChromeOS devices)", Scopes: gScopes("admin.directory.user", "admin.directory.group", "admin.directory.device.chromeos"), OptIn: true},
		},
		Members: []umbrellaMember{
			{Platform: "google_sheets", Group: "sheets"},
			{Platform: "gmail", Group: "gmail"},
			{Platform: "google_drive", Group: "drive"},
			{Platform: "youtube", Group: "youtube"},
		},
	},
	"microsoft": {
		Always: append(msScopes("User.Read"), "offline_access"),
		Groups: []ScopeGroup{
			{Name: "mail", Description: "Outlook mail (read, send)", Scopes: msScopes("Mail.ReadWrite", "Mail.Send")},
			{Name: "calendar", Description: "Outlook calendars", Scopes: msScopes("Calendars.ReadWrite")},
			{Name: "files", Description: "OneDrive files", Scopes: msScopes("Files.ReadWrite.All")},
			{Name: "sites", Description: "SharePoint sites and Excel workbooks", Scopes: msScopes("Sites.ReadWrite.All")},
			{Name: "teams", Description: "Teams chats, channel messages, online meetings and activity notifications", Scopes: msScopes("Team.ReadBasic.All", "Channel.ReadBasic.All", "ChannelMessage.Send", "Chat.ReadWrite", "OnlineMeetings.ReadWrite", "TeamsActivity.Send")},
			{Name: "tasks", Description: "Microsoft To Do and Planner tasks", Scopes: msScopes("Tasks.ReadWrite")},
			{Name: "contacts", Description: "Outlook contacts", Scopes: msScopes("Contacts.ReadWrite")},
			// Opt-in: these scopes need a tenant admin's consent, so a plain
			// connect (and personal accounts) never asks for them.
			{Name: "teams_admin", Description: "Teams channel management and reading channel messages (admin consent)", Scopes: msScopes("ChannelMessage.Read.All", "Channel.Create", "ChannelSettings.ReadWrite.All", "ChatMember.ReadWrite", "ChannelMessage.ReadWrite", "Channel.Delete.All"), OptIn: true},
			{Name: "directory", Description: "Entra ID users and groups (admin consent)", Scopes: msScopes("User.ReadWrite.All", "Group.ReadWrite.All"), OptIn: true},
			{Name: "security", Description: "Microsoft Graph Security secure scores (admin consent)", Scopes: msScopes("SecurityEvents.ReadWrite.All"), OptIn: true},
		},
		Members: []umbrellaMember{
			{Platform: "outlook", Group: "mail"},
		},
	},
}

// umbrellaOrder is the display order of umbrella platforms in listings.
var umbrellaOrder = []string{"google", "microsoft"}

func init() {
	Registry["google"] = PlatformDef{
		ID:         "google",
		Name:       "Google (all products)",
		Category:   "service",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodOAuth},
		Fields:     map[AuthMethod][]CredentialField{},
		OAuth: &OAuthConfig{
			AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL:     "https://oauth2.googleapis.com/token",
			Scopes:       mustResolveScopes("google", nil),
			CallbackPort: 9876,
			// include_granted_scopes: a re-consent that adds a scope group
			// keeps every scope granted before (Google incremental auth).
			ExtraParams: map[string]string{"access_type": "offline", "prompt": "consent", "include_granted_scopes": "true"},
		},
		IconEmoji: "🔷",
	}
	Registry["microsoft"] = PlatformDef{
		ID:         "microsoft",
		Name:       "Microsoft 365 (all products)",
		Category:   "service",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodOAuth},
		Fields:     map[AuthMethod][]CredentialField{},
		// Same Graph OAuth setup as the outlook platform: /common/ serves
		// personal and work/school accounts, and the token exchange retries
		// /consumers/ or /organizations/ when the Azure app is single-audience
		// (PostTokenRequestWithAudienceFallback). PKCE is always on.
		OAuth: &OAuthConfig{
			AuthURL:      "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
			TokenURL:     "https://login.microsoftonline.com/common/oauth2/v2.0/token",
			Scopes:       mustResolveScopes("microsoft", nil),
			CallbackPort: 9876,
		},
		IconEmoji: "🟦",
	}
}

// IsUmbrella reports whether platform is an umbrella platform.
func IsUmbrella(platform string) bool {
	_, ok := umbrellas[platform]
	return ok
}

// UmbrellaPlatforms returns the umbrella platform IDs in display order.
func UmbrellaPlatforms() []string {
	return append([]string(nil), umbrellaOrder...)
}

// UmbrellaFor returns the umbrella platform that also serves a legacy
// per-product platform (gmail → google, outlook → microsoft).
func UmbrellaFor(platform string) (string, bool) {
	for id, u := range umbrellas {
		for _, m := range u.Members {
			if m.Platform == platform {
				return id, true
			}
		}
	}
	return "", false
}

// CompatiblePlatforms lists the connection platforms whose connections a
// node with this credential_platform can use: the platform itself, then its
// umbrella when it has one.
func CompatiblePlatforms(platform string) []string {
	out := []string{platform}
	if u, ok := UmbrellaFor(platform); ok {
		out = append(out, u)
	}
	return out
}

// ScopeGroups returns the scope-group catalogue of an umbrella platform
// (nil for any other platform).
func ScopeGroups(platform string) []ScopeGroup {
	u, ok := umbrellas[platform]
	if !ok {
		return nil
	}
	return append([]ScopeGroup(nil), u.Groups...)
}

// legacyMembers returns the per-product platforms an umbrella stands in for.
func legacyMembers(platform string) []string {
	u := umbrellas[platform]
	out := make([]string, 0, len(u.Members))
	for _, m := range u.Members {
		out = append(out, m.Platform)
	}
	return out
}
