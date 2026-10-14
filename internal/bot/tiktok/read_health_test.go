//go:build !nosocial

package tiktok

// Offline tests of the TikTok read paths (profile videos, comments, search,
// followers, profile data, conversations) driven by healthPage with canned
// page-script results.

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func healthField(rows []map[string]interface{}, key string) []string {
	var out []string
	for _, r := range rows {
		s, _ := r[key].(string)
		out = append(out, s)
	}
	return out
}

// --- profile videos -----------------------------------------------------------

func TestHealthListUserVideosScrollsUntilStale(t *testing.T) {
	healthFast(t)
	p := newHealthPage(t).scriptOpen().
		onValue(hsTabs, map[string]interface{}{"found": true, "selected": false}).
		onSeq(hsUserVideos, healthFixture(t, "read", "videos_round1"), healthFixture(t, "read", "videos_round2")).
		onValue(hsScrollWin, true)
	rows, err := (&TikTokBot{}).ListUserVideos(context.Background(), p, "@synth.ada", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := healthField(rows, "id"); !reflect.DeepEqual(got, []string{"111", "222", "333"}) {
		t.Errorf("ids = %v", got)
	}
	if got := p.navigations(); !reflect.DeepEqual(got, []string{"https://www.tiktok.com/@synth.ada"}) {
		t.Errorf("navigations = %v", got)
	}
	// Two growing reads, then three stale ones.
	if got := p.count(hsUserVideos); got != 5 {
		t.Errorf("grid read %d times, want 5", got)
	}
	if p.clickCount() != 1 {
		t.Errorf("clicks = %d, want 1 (the Videos tab)", p.clickCount())
	}
	if p.count(hsHold) != 1 || p.count(hsRelease) != 1 {
		t.Errorf("videos held %d / released %d times, want 1/1", p.count(hsHold), p.count(hsRelease))
	}
}

func TestHealthListUserVideosMaxAndEmpty(t *testing.T) {
	healthFast(t)
	p := newHealthPage(t).scriptOpen().
		onValue(hsTabs, map[string]interface{}{"found": true, "selected": true}).
		onValue(hsUserVideos, healthFixture(t, "read", "videos_round2"))
	rows, err := (&TikTokBot{}).ListUserVideos(context.Background(), p, "https://www.tiktok.com/@synth.ada?lang=en", 2)
	if err != nil || len(rows) != 2 {
		t.Fatalf("= %d rows, %v; want 2", len(rows), err)
	}
	if p.clickCount() != 0 {
		t.Error("clicked an already selected Videos tab")
	}

	p = newHealthPage(t).scriptOpen().
		onValue(hsTabs, map[string]interface{}{"found": false}).
		onValue(hsUserVideos, healthFixture(t, "read", "videos_empty_grid")).onValue(hsScrollWin, true)
	rows, err = (&TikTokBot{}).ListUserVideos(context.Background(), p, "synth.ada", 5)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Errorf("empty grid = %#v, %v; want an empty list", rows, err)
	}
}

func TestHealthListUserVideosErrors(t *testing.T) {
	healthFast(t)
	b := (&TikTokBot{})
	ctx := context.Background()
	for _, target := range []string{"", "  ", "a b", "@"} {
		if _, err := b.ListUserVideos(ctx, newHealthPage(t), target, 5); err == nil {
			t.Errorf("target %q: want error", target)
		}
	}
	p := newHealthPage(t).scriptOpen().
		onValue(hsTabs, map[string]interface{}{"found": false}).
		onValue(hsUserVideos, healthFixture(t, "read", "videos_none")).onValue(hsScrollWin, true)
	if _, err := b.ListUserVideos(ctx, p, "secret.one", 5); err == nil || !strings.Contains(err.Error(), "no video grid") {
		t.Errorf("no grid: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsTabs, errors.New("threw"))
	if _, err := b.ListUserVideos(ctx, p, "x.y", 5); err == nil || !strings.Contains(err.Error(), "read profile tabs") {
		t.Errorf("tab script: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsTabs, map[string]interface{}{"found": false}).onValue(hsUserVideos, errors.New("threw"))
	if _, err := b.ListUserVideos(ctx, p, "x.y", 5); err == nil || !strings.Contains(err.Error(), "read video grid") {
		t.Errorf("grid script: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsTabs, map[string]interface{}{"found": false}).
		onValue(hsUserVideos, healthFixture(t, "read", "videos_round1")).onValue(hsScrollWin, errors.New("scroll failed"))
	if _, err := b.ListUserVideos(ctx, p, "x.y", 5); err == nil || !strings.Contains(err.Error(), "scroll failed") {
		t.Errorf("scroll failure: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen()
	p.navErr = errors.New("offline")
	if _, err := b.ListUserVideos(ctx, p, "x.y", 5); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Errorf("navigation: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen()
	p.loadErr = errors.New("timeout")
	if _, err := b.ListUserVideos(ctx, p, "x.y", 5); err == nil || !strings.Contains(err.Error(), "did not load") {
		t.Errorf("load: err = %v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := b.ListUserVideos(cctx, newHealthPage(t), "x.y", 5); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: err = %v", err)
	}
}

// --- comments ------------------------------------------------------------------------

func TestHealthListVideoCommentsOpensPanel(t *testing.T) {
	healthFast(t)
	p := newHealthPage(t).scriptOpen().
		onValue(hsInspectCmts, map[string]interface{}{"open": false, "icon": true}).
		onSeq(hsPanelOpen, false, true).
		onValue(hsCommentRead, healthFixture(t, "read", "comments")).
		onValue(hsScrollCmts, true)
	rows, err := (&TikTokBot{}).ListVideoComments(context.Background(), p, "/@synth.ada/video/111", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := healthField(rows, "username"); !reflect.DeepEqual(got, []string{"c.alice", "c.bob"}) {
		t.Errorf("usernames = %v", got)
	}
	if rows[0]["videoUrl"] != "https://www.tiktok.com/@synth.ada/video/111" || rows[1]["liked"] != true {
		t.Errorf("rows = %v", rows)
	}
	if p.clickCount() != 1 {
		t.Errorf("clicks = %d, want 1 (comment icon)", p.clickCount())
	}
}

func TestHealthListVideoCommentsErrorsAndEmpty(t *testing.T) {
	healthFast(t)
	b := (&TikTokBot{})
	ctx := context.Background()
	const v = "https://www.tiktok.com/@a/video/1"
	if _, err := b.ListVideoComments(ctx, newHealthPage(t), " ", 5); err == nil {
		t.Error("no URL: want error")
	}
	p := newHealthPage(t).scriptOpen().onValue(hsInspectCmts, map[string]interface{}{"open": false, "icon": false})
	if _, err := b.ListVideoComments(ctx, p, v, 5); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Errorf("comments off: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsInspectCmts, map[string]interface{}{"open": false, "icon": true}).onValue(hsPanelOpen, false)
	if _, err := b.ListVideoComments(ctx, p, v, 5); err == nil || !strings.Contains(err.Error(), "did not open") {
		t.Errorf("panel never opens: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsInspectCmts, errors.New("threw"))
	if _, err := b.ListVideoComments(ctx, p, v, 5); err == nil || !strings.Contains(err.Error(), "inspect comment panel") {
		t.Errorf("inspect: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsInspectCmts, map[string]interface{}{"open": true}).onValue(hsCommentRead, errors.New("threw"))
	if _, err := b.ListVideoComments(ctx, p, v, 5); err == nil || !strings.Contains(err.Error(), "read comments") {
		t.Errorf("read: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsInspectCmts, map[string]interface{}{"open": true}).
		onValue(hsCommentRead, []interface{}{}).onValue(hsScrollCmts, true)
	rows, err := b.ListVideoComments(ctx, p, v, 5)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Errorf("no comments = %#v, %v; want an empty list", rows, err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsInspectCmts, map[string]interface{}{"open": true}).
		onValue(hsCommentRead, healthFixture(t, "read", "comments"))
	if rows, err = b.ListVideoComments(ctx, p, v, 1); err != nil || len(rows) != 1 {
		t.Errorf("max 1 = %d rows, %v", len(rows), err)
	}
}

// --- search ----------------------------------------------------------------------------

func TestHealthSearchVideos(t *testing.T) {
	healthFast(t)
	p := newHealthPage(t).scriptOpen().onValue(hsSearch, healthFixture(t, "read", "search")).onValue(hsScrollWin, true)
	rows, err := (&TikTokBot{}).SearchVideos(context.Background(), p, " cute cats ", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.navigations(); !reflect.DeepEqual(got, []string{"https://www.tiktok.com/search/video?q=cute+cats"}) {
		t.Errorf("navigations = %v", got)
	}
	if len(rows) != 2 || rows[0]["keyword"] != "cute cats" || rows[0]["author_name"] != "Maker One" {
		t.Errorf("rows = %v", rows)
	}

	p = newHealthPage(t).scriptOpen().onValue(hsSearch, healthFixture(t, "read", "search"))
	if rows, err = (&TikTokBot{}).SearchVideos(context.Background(), p, "cats", 1); err != nil || len(rows) != 1 {
		t.Errorf("max 1 = %d, %v", len(rows), err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsSearch, []interface{}{}).onValue(hsScrollWin, true)
	if _, err = (&TikTokBot{}).SearchVideos(context.Background(), p, "zzz", 5); err == nil || !strings.Contains(err.Error(), "no video results") {
		t.Errorf("no results: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsSearch, errors.New("threw"))
	if _, err = (&TikTokBot{}).SearchVideos(context.Background(), p, "zzz", 5); err == nil || !strings.Contains(err.Error(), "read search results") {
		t.Errorf("read: err = %v", err)
	}
	if _, err = (&TikTokBot{}).SearchVideos(context.Background(), p, "  ", 5); err == nil {
		t.Error("empty keyword: want error")
	}
}

// --- followers ------------------------------------------------------------------------

func TestHealthListFollowers(t *testing.T) {
	healthFast(t)
	p := newHealthPage(t).scriptOpen().
		onSeq(hsFollowList, healthFixture(t, "read", "followers_round1"), healthFixture(t, "read", "followers_round1"),
			healthFixture(t, "read", "followers_round2")).
		onValue(hsScrollFollow, true)
	rows, err := (&TikTokBot{}).ListFollowers(context.Background(), p, "synth.ada", "FOLLOWING_FETCH", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := healthField(rows, "username"); !reflect.DeepEqual(got, []string{"fan.one", "fan.two", "fan.three"}) {
		t.Errorf("usernames = %v", got)
	}
	if rows[0]["source"] != "following" || rows[0]["profileUrl"] != "https://www.tiktok.com/@synth.ada" || rows[1]["followState"] != "Friends" {
		t.Errorf("row = %v", rows[0])
	}
	if p.clickCount() != 1 || p.clicks[0] != "[data-e2e='following-count']" {
		t.Errorf("clicks = %v", p.clicks)
	}
}

func TestHealthListFollowersSlowOpenClicksOnceMore(t *testing.T) {
	healthFast(t)
	var p *healthPage
	p = newHealthPage(t).scriptOpen().
		on(hsFollowList, func(c healthCall) interface{} {
			if p.clickCount() < 2 {
				return map[string]interface{}{"open": false, "items": []interface{}{}}
			}
			return healthFixture(t, "read", "followers_round1")
		}).
		onValue(hsScrollFollow, true)
	rows, err := (&TikTokBot{}).ListFollowers(context.Background(), p, "synth.ada", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["source"] != "followers" {
		t.Errorf("rows = %v", rows)
	}
	if p.clickCount() != 2 {
		t.Errorf("clicks = %d, want 2 (one retry)", p.clickCount())
	}
}

func TestHealthListFollowersErrors(t *testing.T) {
	healthFast(t)
	b := (&TikTokBot{})
	ctx := context.Background()
	if _, err := b.ListFollowers(ctx, newHealthPage(t), "synth.ada", "LIKES", 5); err == nil || !strings.Contains(err.Error(), "unknown sourceType") {
		t.Errorf("bad sourceType: err = %v", err)
	}
	p := newHealthPage(t).scriptOpen()
	p.noElem["[data-e2e='followers-count']"] = true
	if _, err := b.ListFollowers(ctx, p, "synth.ada", "followers", 5); err == nil || !strings.Contains(err.Error(), "count not found") {
		t.Errorf("no count: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsFollowList, map[string]interface{}{"open": true, "items": []interface{}{}})
	if _, err := b.ListFollowers(ctx, p, "synth.ada", "followers", 5); err == nil || !strings.Contains(err.Error(), "empty or private") {
		t.Errorf("empty popup: err = %v", err)
	}
	if p.clickCount() != 1 {
		t.Errorf("clicks = %d, want 1 (an open popup is not clicked again)", p.clickCount())
	}
	p = newHealthPage(t).scriptOpen().onValue(hsFollowList, map[string]interface{}{"open": false, "items": []interface{}{}})
	if _, err := b.ListFollowers(ctx, p, "synth.ada", "followers", 5); err == nil || !strings.Contains(err.Error(), "did not open") {
		t.Errorf("never opens: err = %v", err)
	}
	if p.clickCount() != 2 {
		t.Errorf("clicks = %d, want 2 (never more than one extra click)", p.clickCount())
	}
	p = newHealthPage(t).scriptOpen().onSeq(hsFollowList, healthFixture(t, "read", "followers_round1"), errors.New("threw"))
	if _, err := b.ListFollowers(ctx, p, "synth.ada", "followers", 5); err == nil || !strings.Contains(err.Error(), "read followers list") {
		t.Errorf("read: err = %v", err)
	}
}

// --- profile data ---------------------------------------------------------------------

func TestHealthGetProfileDataOverlaysUniversalData(t *testing.T) {
	healthFast(t)
	p := newHealthPage(t).scriptOpen().
		onValue(hsProfile, healthFixture(t, "read", "profile_header")).
		onValue(hsUniversal, healthFixture(t, "read", "universal"))
	fn, _ := (&TikTokBot{}).GetMethodByName("get_profile_data")
	res, err := fn(context.Background(), p, "https://www.tiktok.com/@synth.ada")
	if err != nil {
		t.Fatal(err)
	}
	data := res.(map[string]interface{})
	want := map[string]interface{}{
		"profile_url":         "https://www.tiktok.com/@synth.ada",
		"username":            "synth.ada",
		"handle":              "synth.ada",
		"full_name":           "Ada S.",
		"bio":                 "Makes synthetic clips",
		"profile_picture_url": "https://cdn.example/ada_large.jpg",
		"language":            "en",
		"platform_id":         "6800000000000000001",
		"is_verified":         true,
		"is_private":          false,
		"website":             "https://ada.example",
		"profile_category":    "Education",
		"account_type":        "business",
		"follower_count":      "3456",
		"following_count":     "12",
		"likes_count":         "99000",
		"content_count":       "42",
		"friend_count":        "3",
	}
	for k, v := range want {
		if !reflect.DeepEqual(data[k], v) {
			t.Errorf("%s = %#v, want %#v", k, data[k], v)
		}
	}
	if _, has := data["found"]; has {
		t.Error("internal 'found' leaked")
	}
}

func TestHealthGetProfileDataHeaderOnly(t *testing.T) {
	healthFast(t)
	// The page's embedded data is another account's: ignored.
	other := healthFixture(t, "read", "universal").(map[string]interface{})
	other["user"].(map[string]interface{})["uniqueId"] = "someone.else"
	p := newHealthPage(t).
		onValue(hsProfile, healthFixture(t, "read", "profile_header")).
		onValue(hsUniversal, other)
	p.url = "https://www.tiktok.com/@synth.ada"
	data, err := (&TikTokBot{}).GetProfileData(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{
		"handle":         "synth.ada",
		"full_name":      "Ada Synth",
		"bio":            "", // "No bio yet" placeholder
		"website":        "https://ada.example/shop",
		"follower_count": "3.4K",
	}
	for k, v := range want {
		if !reflect.DeepEqual(data[k], v) {
			t.Errorf("%s = %#v, want %#v", k, data[k], v)
		}
	}
	if !reflect.DeepEqual(data["links"], []map[string]interface{}{{"url": "https://ada.example/shop"}}) {
		t.Errorf("links = %#v", data["links"])
	}

	// No embedded data at all (script returns null): header only.
	p = newHealthPage(t).onValue(hsProfile, healthFixture(t, "read", "profile_header")).onValue(hsUniversal, nil)
	p.url = "https://www.tiktok.com/@synth.ada"
	if data, err = (&TikTokBot{}).GetProfileData(context.Background(), p); err != nil || data["full_name"] != "Ada Synth" {
		t.Errorf("null universal = %v, %v", data, err)
	}
}

func TestHealthGetProfileDataErrors(t *testing.T) {
	healthFast(t)
	b := (&TikTokBot{})
	p := newHealthPage(t).onValue(hsProfile, map[string]interface{}{"found": false})
	p.url = "https://www.tiktok.com/@gone"
	if _, err := b.GetProfileData(context.Background(), p); err == nil || !strings.Contains(err.Error(), "no profile header on https://www.tiktok.com/@gone") {
		t.Errorf("no header: err = %v", err)
	}
	p = newHealthPage(t).onValue(hsProfile, errors.New("threw"))
	if _, err := b.GetProfileData(context.Background(), p); err == nil || !strings.Contains(err.Error(), "read profile") {
		t.Errorf("script: err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.GetProfileData(ctx, newHealthPage(t)); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: err = %v", err)
	}
	fn, _ := b.GetMethodByName("get_profile_data")
	if _, err := fn(context.Background(), newHealthPage(t), "bad target"); err == nil {
		t.Error("bad target: want error")
	}
}

func TestHealthProfileNamesAndLinks(t *testing.T) {
	for _, c := range []struct{ url, title, sub, handle, name string }{
		{"", "Name", "@h", "h", "Name"},
		{"h", "Name", "@H", "H", "Name"},
		{"h", "@h", "Name", "h", "Name"},
		{"h", "Other", "Thing", "h", "Other"},
	} {
		h, n := profileNames(c.url, c.title, c.sub)
		if h != c.handle || n != c.name {
			t.Errorf("profileNames(%q,%q,%q) = %q,%q; want %q,%q", c.url, c.title, c.sub, h, n, c.handle, c.name)
		}
	}
	for in, want := range map[string]string{
		"https://www.tiktok.com/link/v2?target=https%3A%2F%2Fa.example": "https://a.example",
		"https://www.tiktok.com/link/v2?aid=1":                          "https://www.tiktok.com/link/v2?aid=1",
		"https://a.example/x":                                           "https://a.example/x",
		"%zz":                                                           "%zz",
	} {
		if got := unwrapBioLink(in); got != want {
			t.Errorf("unwrapBioLink(%q) = %q, want %q", in, got, want)
		}
	}
	if withScheme("") != "" || withScheme("x.example") != "https://x.example" || withScheme("http://x") != "http://x" {
		t.Error("withScheme")
	}
	org := universalProfile{}
	org.User.UniqueID, org.User.IsOrganization, org.User.Signature = "o", 1, "No bio yet."
	d := map[string]interface{}{"bio": "keep", "is_verified": true}
	org.apply(d)
	if d["account_type"] != "organization" || d["bio"] != "keep" || d["is_verified"] != true {
		t.Errorf("org apply = %v", d)
	}
}

// --- conversations / basics ----------------------------------------------------------

func TestHealthListConversations(t *testing.T) {
	healthFast(t)
	b := (&TikTokBot{})
	p := newHealthPage(t).onValue(hsConversations, healthFixture(t, "read", "conversations"))
	fn, _ := b.GetMethodByName("list_conversations")
	res, err := fn(context.Background(), p, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := healthField(res.([]map[string]interface{}), "name"); !reflect.DeepEqual(got, []string{"Alice", "Cara"}) {
		t.Errorf("unread (default) = %v", got)
	}
	rows, err := b.ListConversations(context.Background(), p, 2, false)
	if err != nil || !reflect.DeepEqual(healthField(rows, "name"), []string{"Alice", "Bob"}) {
		t.Errorf("all, max 2 = %v, %v", rows, err)
	}
	p = newHealthPage(t)
	p.race = func([]string) (int, error) { return 0, errors.New("timeout") }
	if _, err := b.ListConversations(context.Background(), p, 0, false); err == nil || !strings.Contains(err.Error(), "conversation list not found") {
		t.Errorf("no list: err = %v", err)
	}
	p = newHealthPage(t).onValue(hsConversations, errors.New("threw"))
	if _, err := b.ListConversations(context.Background(), p, 0, false); err == nil || !strings.Contains(err.Error(), "read conversations") {
		t.Errorf("read: err = %v", err)
	}
}

func TestHealthIsLoggedInAndMethods(t *testing.T) {
	b := (&TikTokBot{})
	p := newHealthPage(t)
	if ok, err := b.IsLoggedIn(p); ok || err != nil {
		t.Errorf("no signals = %v, %v", ok, err)
	}
	p.has["[data-e2e='profile-icon']"] = true
	if ok, _ := b.IsLoggedIn(p); !ok {
		t.Error("profile icon: want logged in")
	}
	p.has["[data-e2e='top-login-button']"] = true
	if ok, _ := b.IsLoggedIn(p); ok {
		t.Error("login button wins: want logged out")
	}
	if b.Platform() != "TIKTOK" || b.LoginURL() == "" || b.SearchURL(" a b ") != "https://www.tiktok.com/search/user?q=a+b" {
		t.Error("basics")
	}
	for _, name := range []string{"list_user_videos", "list_video_comments", "get_profile_data", "search_videos", "list_followers",
		"list_conversations", "like_video", "like_comment", "comment_on_video", "follow_user", "send_dm",
		"reply_to_conversation", "publish_uploaded_video", "stitch_video", "duet_video", "share_video"} {
		fn, ok := b.GetMethodByName(name)
		if !ok {
			t.Fatalf("%s missing", name)
		}
		if _, err := fn(context.Background()); err == nil {
			t.Errorf("%s without a page: want error", name)
		}
	}
	if _, ok := b.GetMethodByName("nope"); ok {
		t.Error("unknown method resolved")
	}
	if atoiDefault("x", 7) != 7 || atoiDefault("-1", 7) != 7 || atoiDefault(" 3 ", 7) != 3 {
		t.Error("atoiDefault")
	}
	if !truthy("", true) || !truthy("Yes", false) || truthy("no", true) {
		t.Error("truthy")
	}
}
