package connections

// N8nCredentialMapping says how an n8n credential type maps onto our
// connection platforms, for the n8n workflow importer.
type N8nCredentialMapping struct {
	// Platform is the connection platform an imported node's credential_id
	// should name. A per-product platform (gmail, outlook, …) resolves to
	// that platform's connection or, failing that, its umbrella's.
	Platform string
	// Umbrella is the umbrella platform (google, microsoft) that can also
	// serve the credential; empty when there is none.
	Umbrella string
	// ScopeGroups are the umbrella scope groups the credential's nodes need,
	// for the importer's `monoagentcli connect <umbrella> --scopes …` hint.
	ScopeGroups []string
	// Note is an import caveat shown to the user; empty when the mapping is
	// exact.
	Note string
}

// N8nCredentials maps n8n credential type names to connection platforms.
// Only the Google and Microsoft families are listed here; declarative API
// packages contribute their own mappings.
var N8nCredentials = map[string]N8nCredentialMapping{
	// Google
	"googleApi": {
		Platform: "google", Umbrella: "google",
		Note: "n8n service-account (JWT) credential; connect a Google account with `monoagentcli connect google` instead",
	},
	"googleOAuth2Api":                         {Platform: "google", Umbrella: "google"},
	"googleSheetsOAuth2Api":                   {Platform: "google_sheets", Umbrella: "google", ScopeGroups: []string{"sheets"}},
	"googleSheetsTriggerOAuth2Api":            {Platform: "google_sheets", Umbrella: "google", ScopeGroups: []string{"sheets", "drive"}},
	"gmailOAuth2":                             {Platform: "gmail", Umbrella: "google", ScopeGroups: []string{"gmail"}},
	"googleDriveOAuth2Api":                    {Platform: "google_drive", Umbrella: "google", ScopeGroups: []string{"drive"}},
	"youTubeOAuth2Api":                        {Platform: "youtube", Umbrella: "google", ScopeGroups: []string{"youtube"}},
	"googleCalendarOAuth2Api":                 {Platform: "google", Umbrella: "google", ScopeGroups: []string{"calendar"}},
	"googleDocsOAuth2Api":                     {Platform: "google", Umbrella: "google", ScopeGroups: []string{"docs", "drive"}},
	"googleTasksOAuth2Api":                    {Platform: "google", Umbrella: "google", ScopeGroups: []string{"tasks"}},
	"googleContactsOAuth2Api":                 {Platform: "google", Umbrella: "google", ScopeGroups: []string{"contacts"}},
	"googleTranslateOAuth2Api":                {Platform: "google", Umbrella: "google", ScopeGroups: []string{"translate"}},
	"googleCloudNaturalLanguageOAuth2Api":     {Platform: "google", Umbrella: "google", ScopeGroups: []string{"naturallanguage"}},
	"googleChatOAuth2Api":                     {Platform: "google", Umbrella: "google", ScopeGroups: []string{"chat"}},
	"googleBigQueryOAuth2Api":                 {Platform: "google", Umbrella: "google", ScopeGroups: []string{"bigquery"}},
	"googleCloudStorageOAuth2Api":             {Platform: "google", Umbrella: "google", ScopeGroups: []string{"cloudstorage"}},
	"googleFirebaseCloudFirestoreOAuth2Api":   {Platform: "google", Umbrella: "google", ScopeGroups: []string{"firebase"}},
	"googleFirebaseRealtimeDatabaseOAuth2Api": {Platform: "google", Umbrella: "google", ScopeGroups: []string{"firebase"}},
	"googleAdsOAuth2Api": {
		Platform: "google", Umbrella: "google", ScopeGroups: []string{"ads"},
		Note: "Google Ads also needs a developer token; add it to the node's parameters",
	},
	"googleAnalyticsOAuth2":          {Platform: "google", Umbrella: "google", ScopeGroups: []string{"analytics"}},
	"googleBooksOAuth2Api":           {Platform: "google", Umbrella: "google", ScopeGroups: []string{"books"}},
	"googleBusinessProfileOAuth2Api": {Platform: "google", Umbrella: "google", ScopeGroups: []string{"businessprofile"}},
	"googlePerspectiveOAuth2Api": {
		Platform: "google", Umbrella: "google",
		Note: "Perspective needs no API scope beyond the account email every google connection has",
	},
	"gSuiteAdminOAuth2Api": {Platform: "google", Umbrella: "google", ScopeGroups: []string{"admin"}},
	"googleSlidesOAuth2Api": {
		Platform: "google", Umbrella: "google", ScopeGroups: []string{"drive"},
		Note: "the Slides API accepts the Drive scope; there is no separate slides scope group",
	},

	// Microsoft
	"microsoftOAuth2Api":        {Platform: "microsoft", Umbrella: "microsoft"},
	"microsoftOutlookOAuth2Api": {Platform: "outlook", Umbrella: "microsoft", ScopeGroups: []string{"mail"}},
	"microsoftExcelOAuth2Api":   {Platform: "microsoft", Umbrella: "microsoft", ScopeGroups: []string{"files", "sites"}},
	"microsoftOneDriveOAuth2Api": {
		Platform: "microsoft", Umbrella: "microsoft", ScopeGroups: []string{"files"},
	},
	"microsoftSharePointOAuth2Api":    {Platform: "microsoft", Umbrella: "microsoft", ScopeGroups: []string{"sites"}},
	"microsoftTeamsOAuth2Api":         {Platform: "microsoft", Umbrella: "microsoft", ScopeGroups: []string{"teams"}},
	"microsoftToDoOAuth2Api":          {Platform: "microsoft", Umbrella: "microsoft", ScopeGroups: []string{"tasks"}},
	"microsoftEntraOAuth2Api":         {Platform: "microsoft", Umbrella: "microsoft", ScopeGroups: []string{"directory"}},
	"microsoftGraphSecurityOAuth2Api": {Platform: "microsoft", Umbrella: "microsoft", ScopeGroups: []string{"security"}},
}
