package connections

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveScopesDefaultIsUnionOfDefaultGroups(t *testing.T) {
	for _, platform := range UmbrellaPlatforms() {
		got, err := ResolveScopes(platform, nil)
		if err != nil {
			t.Fatalf("%s: %v", platform, err)
		}
		have := map[string]int{}
		for _, s := range got {
			have[s]++
		}
		for s, n := range have {
			if n != 1 {
				t.Errorf("%s: scope %q listed %d times", platform, s, n)
			}
		}
		for _, s := range umbrellas[platform].Always {
			if have[s] == 0 {
				t.Errorf("%s: always-on scope %q missing", platform, s)
			}
		}
		for _, g := range ScopeGroups(platform) {
			for _, s := range g.Scopes {
				if !g.OptIn && have[s] == 0 {
					t.Errorf("%s: default scopes miss group %s scope %q", platform, g.Name, s)
				}
				if g.OptIn && have[s] > 0 && !inList(umbrellas[platform].Always, s) {
					t.Errorf("%s: default scopes include opt-in group %s scope %q", platform, g.Name, s)
				}
			}
		}
		if reg := Registry[platform].OAuth.Scopes; strings.Join(reg, " ") != strings.Join(got, " ") {
			t.Errorf("%s: registry scopes %v != default union %v", platform, reg, got)
		}
	}
}

func TestGoogleDefaultCoversEveryLegacyProductScope(t *testing.T) {
	scopes, _ := ResolveScopes("google", nil)
	for _, legacy := range []string{"google_sheets", "gmail", "google_drive", "youtube"} {
		if missing := missingScopes("google", scopes, Registry[legacy].OAuth.Scopes); len(missing) > 0 {
			t.Errorf("google default lacks %s scopes %v", legacy, missing)
		}
	}
	for _, want := range []string{"calendar.events", "documents", "forms.body.readonly", "forms.responses.readonly", "tasks", "contacts"} {
		if missing := missingScopes("google", scopes, []string{googleScopePrefix + want}); len(missing) > 0 {
			t.Errorf("google default lacks %s", want)
		}
	}
	ms, _ := ResolveScopes("microsoft", nil)
	if missing := missingScopes("microsoft", ms, Registry["outlook"].OAuth.Scopes); len(missing) > 0 {
		t.Errorf("microsoft default lacks outlook scopes %v", missing)
	}
	for _, want := range []string{"Mail.ReadWrite", "Calendars.ReadWrite", "Files.ReadWrite.All", "Sites.ReadWrite.All", "ChannelMessage.Send", "Chat.ReadWrite", "offline_access"} {
		if missing := missingScopes("microsoft", ms, []string{want}); len(missing) > 0 {
			t.Errorf("microsoft default lacks %s", want)
		}
	}
}

func TestResolveScopesNarrowing(t *testing.T) {
	got, err := ResolveScopes("google", []string{" Sheets", "gmail", "", "calendar"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"openid", googleScopePrefix + "userinfo.email",
		googleScopePrefix + "spreadsheets", googleScopePrefix + "drive.readonly",
		googleScopePrefix + "gmail.modify",
		googleScopePrefix + "calendar.events",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("narrowed scopes\n got %v\nwant %v", got, want)
	}

	ms, err := ResolveScopes("microsoft", []string{"teams"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(ms, " "), "Mail.") {
		t.Errorf("teams-only microsoft scopes include mail: %v", ms)
	}
	if !strings.Contains(strings.Join(ms, " "), "offline_access") {
		t.Errorf("microsoft scopes always need offline_access: %v", ms)
	}

	if _, err := ResolveScopes("google", []string{"sheets", "calender"}); err == nil ||
		!strings.Contains(err.Error(), `unknown google scope group "calender"`) || !strings.Contains(err.Error(), "calendar") {
		t.Errorf("unknown group error = %v", err)
	}
	if _, err := ResolveScopes("gmail", []string{"gmail"}); err == nil {
		t.Error("scope groups on a non-umbrella platform must error")
	}
}

func TestPrepareUmbrellaOAuth(t *testing.T) {
	cfg := *Registry["google"].OAuth
	if err := prepareUmbrellaOAuth(&cfg, "google", []string{"docs"}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Scopes) != 3 || cfg.Scopes[2] != googleScopePrefix+"documents" {
		t.Errorf("docs scopes = %v", cfg.Scopes)
	}
	if Registry["google"].OAuth.Scopes[2] == googleScopePrefix+"documents" {
		t.Error("narrowing must not mutate the registry entry")
	}
	gm := *Registry["gmail"].OAuth
	if err := prepareUmbrellaOAuth(&gm, "gmail", nil); err != nil {
		t.Errorf("no scopes on a legacy platform: %v", err)
	}
	if err := prepareUmbrellaOAuth(&gm, "gmail", []string{"gmail"}); err == nil {
		t.Error("--scopes on a legacy platform must error")
	}
}

func TestCheckCredentialScopes(t *testing.T) {
	gscope := func(names ...string) string { return strings.Join(gScopes(names...), " ") }

	// Per-product (legacy) connections are never judged.
	for _, product := range []string{"google_sheets", "gmail", "google_drive", "youtube", "outlook"} {
		legacy := map[string]interface{}{"access_token": "t", "scope": "something-else"}
		if err := CheckCredentialScopes(legacy, product); err != nil {
			t.Errorf("legacy %s connection rejected: %v", product, err)
		}
	}
	if err := CheckCredentialScopes(nil, "gmail"); err != nil {
		t.Errorf("nil credential: %v", err)
	}

	// Umbrella connection with unknown grants passes.
	if err := CheckCredentialScopes(map[string]interface{}{"oauth_platform": "google"}, "gmail"); err != nil {
		t.Errorf("unknown grants: %v", err)
	}

	sheetsGmail := map[string]interface{}{
		"oauth_platform": "google",
		"scope":          "openid " + gscope("userinfo.email", "spreadsheets", "drive.readonly", "gmail.modify"),
	}
	for _, product := range []string{"google_sheets", "gmail"} {
		if err := CheckCredentialScopes(sheetsGmail, product); err != nil {
			t.Errorf("%s granted but rejected: %v", product, err)
		}
	}
	err := CheckCredentialScopes(sheetsGmail, "calendar")
	var se *ScopeError
	if !errors.As(err, &se) {
		t.Fatalf("missing calendar: err = %v, want *ScopeError", err)
	}
	wantMsg := `the google connection was not granted the "calendar" scope group (missing https://www.googleapis.com/auth/calendar.events); add it with: monoagentcli connect google --scopes sheets,gmail,calendar`
	if err.Error() != wantMsg {
		t.Errorf("re-consent error\n got %s\nwant %s", err.Error(), wantMsg)
	}
	if err := CheckCredentialScopes(sheetsGmail, "google_drive"); err == nil ||
		!strings.Contains(err.Error(), "monoagentcli connect google --scopes sheets,gmail,drive") {
		t.Errorf("missing drive error = %v", err)
	}
	if err := CheckCredentialScopes(sheetsGmail, "youtube"); err == nil ||
		!strings.Contains(err.Error(), "--scopes sheets,gmail,youtube") {
		t.Errorf("missing youtube error = %v", err)
	}

	// Broader grants cover narrower requirements.
	broad := map[string]interface{}{"oauth_platform": "google", "scope": "https://mail.google.com/ " + gscope("drive", "calendar")}
	for _, product := range []string{"gmail", "google_drive", "calendar"} {
		if err := CheckCredentialScopes(broad, product); err != nil {
			t.Errorf("broad grant rejected for %s: %v", product, err)
		}
	}
	if err := CheckCredentialScopes(broad, "google_sheets"); err == nil {
		t.Error("drive alone must not satisfy spreadsheets")
	}

	// Microsoft returns short or URL-prefixed scope names.
	ms := map[string]interface{}{"oauth_platform": "microsoft", "scope": "Mail.ReadWrite Mail.Send User.Read offline_access"}
	if err := CheckCredentialScopes(ms, "outlook"); err != nil {
		t.Errorf("outlook via microsoft rejected: %v", err)
	}
	err = CheckCredentialScopes(ms, "teams")
	if err == nil || !strings.Contains(err.Error(), "monoagentcli connect microsoft --scopes mail,teams") {
		t.Errorf("missing teams error = %v", err)
	}
	all, _ := ResolveScopes("microsoft", []string{"all"})
	full := map[string]interface{}{"oauth_platform": "microsoft", "scope": strings.Join(all, " ")}
	for _, g := range ScopeGroups("microsoft") {
		if err := CheckCredentialScopes(full, g.Name); err != nil {
			t.Errorf("full microsoft grant rejected for %s: %v", g.Name, err)
		}
	}
}

func TestScopeCoversMicrosoftRules(t *testing.T) {
	cases := []struct {
		g, r string
		want bool
	}{
		{"Files.ReadWrite.All", "Files.Read.All", true},
		{"Files.ReadWrite.All", "Files.ReadWrite", true},
		{"Files.ReadWrite", "Files.ReadWrite.All", false},
		{"https://graph.microsoft.com/Mail.ReadWrite", "Mail.Read", true},
		{"Mail.Read", "Mail.ReadWrite", false},
	}
	for _, c := range cases {
		if got := scopeCovers("microsoft", c.g, c.r); got != c.want {
			t.Errorf("scopeCovers(%q, %q) = %v, want %v", c.g, c.r, got, c.want)
		}
	}
}

func TestUmbrellaMembership(t *testing.T) {
	for legacy, want := range map[string]string{"google_sheets": "google", "gmail": "google", "google_drive": "google", "youtube": "google", "outlook": "microsoft"} {
		if got, ok := UmbrellaFor(legacy); !ok || got != want {
			t.Errorf("UmbrellaFor(%s) = %q, want %q", legacy, got, want)
		}
		if c := CompatiblePlatforms(legacy); len(c) != 2 || c[0] != legacy || c[1] != want {
			t.Errorf("CompatiblePlatforms(%s) = %v", legacy, c)
		}
	}
	if c := CompatiblePlatforms("slack"); len(c) != 1 {
		t.Errorf("CompatiblePlatforms(slack) = %v", c)
	}
}

func TestValidateUmbrellaAccounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/userinfo":
			_, _ = w.Write([]byte(`{"sub":"123","email":"me@example.com"}`))
		case "/me":
			if r.URL.Query().Get("$select") != "mail,userPrincipalName" {
				t.Errorf("me query = %q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"mail":null,"userPrincipalName":"me@contoso.com"}`))
		}
	}))
	defer srv.Close()
	oldG, oldM := googleUserinfoURL, microsoftMeURL
	googleUserinfoURL, microsoftMeURL = srv.URL+"/userinfo", srv.URL+"/me?$select=mail,userPrincipalName"
	t.Cleanup(func() { googleUserinfoURL, microsoftMeURL = oldG, oldM })

	ctx := context.Background()
	if id, err := ValidateConnection(ctx, &Connection{Platform: "google", Method: MethodOAuth, Data: map[string]interface{}{"access_token": "tok"}}); err != nil || id != "me@example.com" {
		t.Errorf("google validate = %q, %v", id, err)
	}
	if id, err := ValidateConnection(ctx, &Connection{Platform: "microsoft", Method: MethodOAuth, Data: map[string]interface{}{"access_token": "tok"}}); err != nil || id != "me@contoso.com" {
		t.Errorf("microsoft validate = %q, %v", id, err)
	}
	if _, err := ValidateConnection(ctx, &Connection{Platform: "google", Method: MethodOAuth, Data: map[string]interface{}{"access_token": "bad"}}); err == nil {
		t.Error("401 must fail validation")
	}
}

func TestN8nCredentialMappings(t *testing.T) {
	for _, name := range []string{
		"googleApi", "googleOAuth2Api", "googleSheetsOAuth2Api", "gmailOAuth2", "googleDriveOAuth2Api",
		"googleCalendarOAuth2Api", "googleDocsOAuth2Api", "youTubeOAuth2Api",
		"microsoftOAuth2Api", "microsoftOutlookOAuth2Api", "microsoftExcelOAuth2Api",
		"microsoftTeamsOAuth2Api", "microsoftOneDriveOAuth2Api",
	} {
		if _, ok := N8nCredentials[name]; !ok {
			t.Errorf("n8n credential %q not mapped", name)
		}
	}
	for name, m := range N8nCredentials {
		if _, ok := Registry[m.Platform]; !ok {
			t.Errorf("%s: platform %q not registered", name, m.Platform)
		}
		if m.Umbrella != "" && !IsUmbrella(m.Umbrella) {
			t.Errorf("%s: umbrella %q is not an umbrella platform", name, m.Umbrella)
		}
		if m.Platform != m.Umbrella {
			if u, _ := UmbrellaFor(m.Platform); u != m.Umbrella {
				t.Errorf("%s: platform %q is not served by umbrella %q", name, m.Platform, m.Umbrella)
			}
		}
		if _, err := ResolveScopes(m.Umbrella, m.ScopeGroups); len(m.ScopeGroups) > 0 && err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
