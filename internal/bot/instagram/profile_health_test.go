//go:build !nosocial

package instagram

// Offline tests of the Instagram profile read paths (page store, the
// web_profile_info endpoint, the rendered header) and metadata helpers,
// driven by healthPage with canned page-script results.

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestHealthGetUserInfoFromStore(t *testing.T) {
	healthFast(t)
	p := newHealthPage(t).scriptOpen().onValue(hsStore, healthFixture(t, "profile", "store"))
	res, err := New().GetUserInfo(context.Background(), p, "synth.ada")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.navigations(); !reflect.DeepEqual(got, []string{"https://www.instagram.com/synth.ada/"}) {
		t.Errorf("navigations = %v", got)
	}
	want := map[string]interface{}{
		"platform":         "INSTAGRAM",
		"username":         "synth.ada",
		"url":              "https://www.instagram.com/synth.ada/",
		"full_name":        "Ada Synth",
		"bio":              "Builds synthetic things",
		"is_verified":      true,
		"is_private":       false,
		"image_url":        "https://cdn.example/ada_hd.jpg",
		"follower_count":   "12345",
		"following_count":  "67",
		"content_count":    "89",
		"platform_id":      "9001",
		"profile_category": "Artist",
		"account_type":     "creator",
		"pronouns":         []string{"she", "her"},
		"threads_handle":   "synth.ada",
		"website":          "https://ada.example/shop",
		"highlights":       []string{"Travel", "Food"},
		"contact": map[string]interface{}{
			"email":   "ada@example.com",
			"phone":   "+1 5551234",
			"address": "1 Synth St, Berlin, 10115",
		},
	}
	for k, v := range want {
		if !reflect.DeepEqual(res[k], v) {
			t.Errorf("%s = %#v, want %#v", k, res[k], v)
		}
	}
	wantLinks := []map[string]interface{}{
		{"url": "https://ada.example/shop", "title": "Shop"},
		{"url": "https://ada.example/blog"},
	}
	if !reflect.DeepEqual(res["links"], wantLinks) {
		t.Errorf("links = %#v (lynx_url unwrapped, duplicates and non-objects dropped)", res["links"])
	}
	if p.count(hsFetchAPI) != 0 || p.count(hsDOM) != 0 {
		t.Error("store hit must not fall through to the API or the DOM")
	}
}

func TestHealthGetUserInfoStoreWaitsForHighlightsTray(t *testing.T) {
	healthFast(t)
	// The record is there but the tray has not resolved: GetUserInfo polls
	// up to six times, then returns what it has.
	rec := healthFixture(t, "profile", "store").(map[string]interface{})
	rec["tray"], rec["highlights"] = false, []interface{}{}
	p := newHealthPage(t).scriptOpen().onValue(hsStore, rec)
	res, err := New().GetUserInfo(context.Background(), p, "synth.ada")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.count(hsStore); got != 6 {
		t.Errorf("store read %d times, want 6", got)
	}
	if _, has := res["highlights"]; has {
		t.Errorf("highlights = %v, want none", res["highlights"])
	}
}

func TestHealthGetUserInfoFallsBackToAPI(t *testing.T) {
	healthFast(t)
	p := newHealthPage(t).scriptOpen().
		onValue(hsStore, map[string]interface{}{"state": "header"}).
		onValue(hsFetchAPI, healthFixture(t, "profile", "api"))
	res, err := New().GetUserInfo(context.Background(), p, "synth.bob")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.count(hsStore); got != 6 {
		t.Errorf("store polled %d times on a header without a record, want 6", got)
	}
	want := map[string]interface{}{
		"full_name":       "Bob Synth",
		"bio":             "Bio from the API",
		"website":         "https://bob.example",
		"image_url":       "https://cdn.example/bob_hd.jpg",
		"follower_count":  "1500",
		"following_count": "20",
		"content_count":   "3",
		"platform_id":     "777",
		"account_type":    "business",
		"contact": map[string]interface{}{
			"email":   "bob@example.com",
			"phone":   "+49 30 1234",
			"address": "2 Fake Rd, Hamburg, 20095",
		},
	}
	for k, v := range want {
		if !reflect.DeepEqual(res[k], v) {
			t.Errorf("%s = %#v, want %#v", k, res[k], v)
		}
	}
	if _, has := res["profile_category"]; has {
		t.Error("a hidden category (should_show_category=false) must not be returned")
	}
}

func TestHealthGetUserInfoFallsBackToRenderedHeader(t *testing.T) {
	healthFast(t)
	api := healthFixture(t, "profile", "api") // username synth.bob ≠ synth.cy
	p := newHealthPage(t).scriptOpen().
		onValue(hsStore, map[string]interface{}{"state": "wait"}).
		onValue(hsFetchAPI, api).
		onSeq(hsDOM, map[string]interface{}{"state": "no_header"}, healthFixture(t, "profile", "dom"))
	res, err := New().GetUserInfo(context.Background(), p, "synth.cy")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{
		"username":         "synth.cy",
		"full_name":        "Cy Synth", // og:title, before JSON-LD
		"bio":              "LD bio",
		"is_private":       true,
		"image_url":        "https://cdn.example/cy.jpg",
		"website":          "https://cy.example/",
		"profile_category": "Musician",
		"follower_count":   "1200", // header empty -> og:description
		"following_count":  "1234", // header wins over og
		"content_count":    "45",
	}
	for k, v := range want {
		if !reflect.DeepEqual(res[k], v) {
			t.Errorf("%s = %#v, want %#v", k, res[k], v)
		}
	}
	if got := p.count(hsDOM); got != 2 {
		t.Errorf("DOM read %d times, want 2 (retried until the header rendered)", got)
	}
}

func TestHealthGetUserInfoAPIErrorsFallThrough(t *testing.T) {
	for name, api := range map[string]interface{}{
		"http error": map[string]interface{}{"error": "HTTP 429"},
		"no user":    map[string]interface{}{"data": map[string]interface{}{"data": map[string]interface{}{}}},
		"graphql":    map[string]interface{}{"data": map[string]interface{}{"graphql": map[string]interface{}{"user": map[string]interface{}{"username": "synth.cy", "full_name": "GQL"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			healthFast(t)
			p := newHealthPage(t).scriptOpen().
				onValue(hsStore, map[string]interface{}{"state": "wait"}).
				onValue(hsFetchAPI, api).
				onValue(hsDOM, healthFixture(t, "profile", "dom"))
			res, err := New().GetUserInfo(context.Background(), p, "synth.cy")
			if err != nil {
				t.Fatal(err)
			}
			wantName := "Cy Synth"
			if name == "graphql" {
				wantName = "GQL"
			}
			if res["full_name"] != wantName {
				t.Errorf("full_name = %v, want %v", res["full_name"], wantName)
			}
		})
	}
}

func TestHealthGetUserInfoErrors(t *testing.T) {
	healthFast(t)
	b := New()
	if _, err := b.GetUserInfo(context.Background(), newHealthPage(t), ""); err == nil {
		t.Error("empty username: want error")
	}

	p := newHealthPage(t)
	p.navErr = errors.New("net down")
	if _, err := b.GetUserInfo(context.Background(), p, "x.y"); err == nil || !strings.Contains(err.Error(), "net down") {
		t.Errorf("navigation error = %v", err)
	}

	p = newHealthPage(t)
	p.loadErr = errors.New("load failed")
	if _, err := b.GetUserInfo(context.Background(), p, "x.y"); err == nil || !strings.Contains(err.Error(), "did not load") {
		t.Errorf("load error = %v", err)
	}

	p = newHealthPage(t).onValue(hsLogin, true)
	if _, err := b.GetUserInfo(context.Background(), p, "x.y"); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("login wall = %v, want ErrNotLoggedIn", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.GetUserInfo(ctx, newHealthPage(t), "x.y"); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled ctx = %v", err)
	}

	// An unavailable profile stops the DOM poll at once.
	p = newHealthPage(t).scriptOpen().
		onValue(hsStore, map[string]interface{}{"state": "wait"}).
		onValue(hsFetchAPI, map[string]interface{}{"error": "HTTP 404"}).
		onValue(hsDOM, healthFixture(t, "profile", "dom_unavailable"))
	if _, err := b.GetUserInfo(context.Background(), p, "gone.user"); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Errorf("unavailable = %v", err)
	}
	if got := p.count(hsDOM); got != 1 {
		t.Errorf("DOM read %d times for an unavailable profile, want 1", got)
	}

	// No header ever: a timeout error naming the profile.
	p = newHealthPage(t).scriptOpen().
		onValue(hsStore, map[string]interface{}{"state": "wait"}).
		onValue(hsFetchAPI, map[string]interface{}{"error": "HTTP 500"}).
		onValue(hsDOM, healthFixture(t, "profile", "dom_no_header"))
	if _, err := b.GetUserInfo(context.Background(), p, "slow.user"); err == nil || !strings.Contains(err.Error(), "slow.user") {
		t.Errorf("no header = %v", err)
	}
}

func TestHealthGetUserInfoSkipsNavigationWhenAlreadyThere(t *testing.T) {
	healthFast(t)
	p := newHealthPage(t).onValue(hsStore, healthFixture(t, "profile", "store"))
	p.url = "https://www.instagram.com/Synth.Ada/"
	if _, err := New().GetUserInfo(context.Background(), p, "synth.ada"); err != nil {
		t.Fatal(err)
	}
	if n := len(p.navigations()); n != 0 {
		t.Errorf("navigated %d times while already on the profile", n)
	}
}

func TestHealthGetMethodGetUserInfoAndProfileData(t *testing.T) {
	healthFast(t)
	b := New()
	fn, ok := b.GetMethodByName("get_user_info")
	if !ok {
		t.Fatal("get_user_info missing")
	}
	if _, err := fn(context.Background(), newHealthPage(t), "https://www.instagram.com/p/ABC/"); err == nil {
		t.Error("a post URL has no username: want error")
	}
	if _, err := fn(context.Background()); err == nil {
		t.Error("no page: want error")
	}
	p := newHealthPage(t).scriptOpen().onValue(hsStore, healthFixture(t, "profile", "store"))
	res, err := fn(context.Background(), p, "@synth.ada")
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := res.(map[string]interface{}); m["username"] != "synth.ada" {
		t.Errorf("res = %v", res)
	}
	if _, ok := b.GetMethodByName("no_such_method"); ok {
		t.Error("unknown method resolved")
	}
}

func TestHealthExtractUsernameFromMetadata(t *testing.T) {
	healthFast(t)
	p := newHealthPage(t).onSeq(hsMeta, "", "synth.meta")
	u, err := New().ExtractUsernameFromMetadata(context.Background(), p)
	if err != nil || u != "synth.meta" {
		t.Fatalf("= %q, %v", u, err)
	}
	p = newHealthPage(t).onValue(hsMeta, "")
	if _, err := New().ExtractUsernameFromMetadata(context.Background(), p); err == nil {
		t.Error("no metadata: want error")
	}
}

func TestHealthIsLoggedInNoSignals(t *testing.T) {
	if ok, err := New().IsLoggedIn(newHealthPage(t)); ok || err != nil {
		t.Errorf("= %v, %v; want false, nil", ok, err)
	}
	if _, err := New().IsLoggedIn(nil); err == nil {
		t.Error("nil page: want error")
	}
}

func TestHealthParseCountAndShim(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "1,234": "1234", "12.5K": "12500", "686m": "686000000", "1b": "1000000000",
		"1,5k": "1500", "abc": "abc", "x k": "x", " 42 ": "42",
	} {
		if got := parseCount(in); got != want {
			t.Errorf("parseCount(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"https://l.instagram.com/?u=https%3A%2F%2Fa.example%2F": "https://a.example/",
		"https://l.instagram.com/?e=1":                          "https://l.instagram.com/?e=1",
		"https://a.example/":                                    "https://a.example/",
		"%zz":                                                   "%zz",
	} {
		if got := unwrapLinkShim(in); got != want {
			t.Errorf("unwrapLinkShim(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHealthAccountTypeAndContact(t *testing.T) {
	for want, user := range map[string]map[string]interface{}{
		"personal": {"account_type": 1.0},
		"business": {"account_type": 2.0},
		"creator":  {"is_professional_account": true},
		"":         {},
	} {
		if got := accountType(user); got != want {
			t.Errorf("accountType(%v) = %q, want %q", user, got, want)
		}
	}
	if c := contactOf(map[string]interface{}{"public_phone_number": "+44 1", "public_phone_country_code": "44"}); c["phone"] != "+44 1" {
		t.Errorf("phone with + must not get the country code again: %v", c)
	}
	if c := contactOf(map[string]interface{}{"business_address_json": "{bad"}); len(c) != 0 {
		t.Errorf("bad address JSON = %v, want no contact", c)
	}
}
