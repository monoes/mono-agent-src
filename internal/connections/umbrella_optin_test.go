package connections

import (
	"strings"
	"testing"
)

func inList(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestGoogleOptInScopeGroups(t *testing.T) {
	want := map[string][]string{
		"translate":       {"cloud-translation"},
		"naturallanguage": {"cloud-language"},
		"chat":            {"chat.messages", "chat.spaces"},
		"bigquery":        {"bigquery"},
		"cloudstorage":    {"devstorage.read_write"},
		"firebase":        {"datastore", "firebase.database", "userinfo.email"},
		"ads":             {"adwords"},
		"analytics":       {"analytics.readonly"},
		"books":           {"books"},
		"businessprofile": {"business.manage"},
		"admin":           {"admin.directory.user", "admin.directory.group", "admin.directory.device.chromeos"},
	}
	groups := map[string]ScopeGroup{}
	for _, g := range ScopeGroups("google") {
		groups[g.Name] = g
	}
	for name, scopes := range want {
		g, ok := groups[name]
		if !ok {
			t.Errorf("google has no %q scope group", name)
			continue
		}
		if !g.OptIn {
			t.Errorf("group %q must be opt-in", name)
		}
		if strings.Join(g.Scopes, " ") != strings.Join(gScopes(scopes...), " ") {
			t.Errorf("group %q scopes = %v, want %v", name, g.Scopes, gScopes(scopes...))
		}
		// Named, an opt-in group is requested on top of the always-on scopes.
		got, err := ResolveScopes("google", []string{name})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, s := range gScopes(scopes...) {
			if !inList(got, s) {
				t.Errorf("--scopes %s lacks %s: %v", name, s, got)
			}
		}
	}
	for _, name := range []string{"sheets", "gmail", "drive", "youtube", "calendar", "docs", "forms", "tasks", "contacts"} {
		if g, ok := groups[name]; !ok || g.OptIn {
			t.Errorf("group %q must exist and be in the default set", name)
		}
	}
	if c := groups["contacts"]; strings.Join(c.Scopes, " ") != googleScopePrefix+"contacts" {
		t.Errorf("contacts scopes = %v, want read-write contacts", c.Scopes)
	}

	def, _ := ResolveScopes("google", nil)
	for _, s := range []string{"cloud-translation", "adwords", "admin.directory.user", "bigquery"} {
		if inList(def, googleScopePrefix+s) {
			t.Errorf("a plain connect must not request %s", s)
		}
	}
	all, _ := ResolveScopes("google", []string{"all"})
	for _, g := range ScopeGroups("google") {
		for _, s := range g.Scopes {
			if !inList(all, s) {
				t.Errorf("--scopes all lacks %s (group %s)", s, g.Name)
			}
		}
	}
	// Mixing a default group and an opt-in one.
	got, _ := ResolveScopes("google", []string{"sheets", "bigquery"})
	if !inList(got, googleScopePrefix+"spreadsheets") || !inList(got, googleScopePrefix+"bigquery") || inList(got, googleScopePrefix+"gmail.modify") {
		t.Errorf("sheets,bigquery = %v", got)
	}
	if h := ScopeGroupsHelp(); !strings.Contains(h, "(opt-in, granted only when named or with all: translate,") || strings.Contains(strings.SplitN(h, "(opt-in", 2)[0], "bigquery") {
		t.Errorf("help = %q", h)
	}
}

func TestOptInGroupScopeCheck(t *testing.T) {
	def, _ := ResolveScopes("google", nil)
	cred := map[string]interface{}{"oauth_platform": "google", "scope": strings.Join(def, " ")}
	err := CheckCredentialScopes(cred, "bigquery")
	se, ok := err.(*ScopeError)
	if !ok || se.Group != "bigquery" || !strings.Contains(se.Command, "bigquery") {
		t.Fatalf("missing opt-in group: %v", err)
	}
	cred["scope"] = cred["scope"].(string) + " " + googleScopePrefix + "bigquery"
	if err := CheckCredentialScopes(cred, "bigquery"); err != nil {
		t.Errorf("granted bigquery: %v", err)
	}
	// A token with only contacts.readonly lacks the read-write contacts group.
	cred["scope"] = googleScopePrefix + "contacts.readonly"
	if err := CheckCredentialScopes(cred, "contacts"); err == nil {
		t.Error("contacts.readonly must not satisfy the read-write contacts group")
	}
}

func TestN8nGoogleCredentialMappings(t *testing.T) {
	want := map[string]string{
		"googleTranslateOAuth2Api":                "translate",
		"googleChatOAuth2Api":                     "chat",
		"googleBigQueryOAuth2Api":                 "bigquery",
		"googleAdsOAuth2Api":                      "ads",
		"googleAnalyticsOAuth2":                   "analytics",
		"googleBooksOAuth2Api":                    "books",
		"googleBusinessProfileOAuth2Api":          "businessprofile",
		"googleCloudNaturalLanguageOAuth2Api":     "naturallanguage",
		"googleCloudStorageOAuth2Api":             "cloudstorage",
		"googleFirebaseCloudFirestoreOAuth2Api":   "firebase",
		"googleFirebaseRealtimeDatabaseOAuth2Api": "firebase",
		"googlePerspectiveOAuth2Api":              "",
		"gSuiteAdminOAuth2Api":                    "admin",
	}
	for cred, group := range want {
		m, ok := N8nCredentials[cred]
		if !ok {
			t.Errorf("N8nCredentials lacks %s", cred)
			continue
		}
		if m.Platform != "google" || m.Umbrella != "google" {
			t.Errorf("%s: platform %q umbrella %q", cred, m.Platform, m.Umbrella)
		}
		if strings.Join(m.ScopeGroups, ",") != group {
			t.Errorf("%s: scope groups %v, want %q", cred, m.ScopeGroups, group)
		}
	}
	// Every scope group an n8n mapping names exists on its umbrella.
	for cred, m := range N8nCredentials {
		for _, g := range m.ScopeGroups {
			if _, ok := productGroup(m.Umbrella, g); !ok {
				t.Errorf("%s names unknown %s scope group %q", cred, m.Umbrella, g)
			}
		}
	}
}

func TestMicrosoftScopeGroups(t *testing.T) {
	groups := map[string]ScopeGroup{}
	for _, g := range ScopeGroups("microsoft") {
		groups[g.Name] = g
	}
	want := map[string]struct {
		scopes []string
		optIn  bool
	}{
		"tasks":       {[]string{"Tasks.ReadWrite"}, false},
		"contacts":    {[]string{"Contacts.ReadWrite"}, false},
		"teams":       {[]string{"Team.ReadBasic.All", "Channel.ReadBasic.All", "ChannelMessage.Send", "Chat.ReadWrite", "OnlineMeetings.ReadWrite", "TeamsActivity.Send"}, false},
		"teams_admin": {[]string{"ChannelMessage.Read.All", "Channel.Create", "ChannelSettings.ReadWrite.All", "ChatMember.ReadWrite", "ChannelMessage.ReadWrite", "Channel.Delete.All"}, true},
		"directory":   {[]string{"User.ReadWrite.All", "Group.ReadWrite.All"}, true},
		"security":    {[]string{"SecurityEvents.ReadWrite.All"}, true},
	}
	for name, w := range want {
		g, ok := groups[name]
		if !ok {
			t.Errorf("microsoft has no %q scope group", name)
			continue
		}
		if g.OptIn != w.optIn {
			t.Errorf("group %q opt-in = %v, want %v", name, g.OptIn, w.optIn)
		}
		if strings.Join(g.Scopes, " ") != strings.Join(msScopes(w.scopes...), " ") {
			t.Errorf("group %q scopes = %v, want %v", name, g.Scopes, msScopes(w.scopes...))
		}
	}
	// A plain connect asks for nothing that needs a tenant admin's consent.
	def, _ := ResolveScopes("microsoft", nil)
	for _, s := range []string{"User.ReadWrite.All", "Group.ReadWrite.All", "SecurityEvents.ReadWrite.All", "ChannelMessage.Read.All", "Channel.Create", "ChannelSettings.ReadWrite.All", "ChatMember.ReadWrite"} {
		if inList(def, microsoftScopePrefix+s) {
			t.Errorf("a plain microsoft connect must not request %s", s)
		}
	}
	for _, s := range []string{"Tasks.ReadWrite", "Contacts.ReadWrite", "OnlineMeetings.ReadWrite", "TeamsActivity.Send"} {
		if !inList(def, microsoftScopePrefix+s) {
			t.Errorf("a plain microsoft connect lacks %s", s)
		}
	}
	got, err := ResolveScopes("microsoft", []string{"directory"})
	if err != nil || !inList(got, microsoftScopePrefix+"Group.ReadWrite.All") || inList(got, microsoftScopePrefix+"Mail.ReadWrite") {
		t.Errorf("--scopes directory = %v, %v", got, err)
	}
	// An Entra op on a default connection names the opt-in group to add.
	cred := map[string]interface{}{"oauth_platform": "microsoft", "scope": strings.Join(def, " ")}
	err = CheckCredentialScopes(cred, "directory")
	if err == nil || !strings.Contains(err.Error(), "--scopes mail,calendar,files,sites,teams,tasks,contacts,directory") {
		t.Errorf("directory on a default connection: %v", err)
	}
	for cred, group := range map[string]string{"microsoftToDoOAuth2Api": "tasks", "microsoftEntraOAuth2Api": "directory", "microsoftGraphSecurityOAuth2Api": "security"} {
		if m := N8nCredentials[cred]; strings.Join(m.ScopeGroups, ",") != group {
			t.Errorf("%s: scope groups %v, want %q", cred, m.ScopeGroups, group)
		}
	}
}
