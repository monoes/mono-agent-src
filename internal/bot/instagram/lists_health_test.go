//go:build !nosocial

package instagram

// Offline tests of the Instagram list readers: followers/following dialog,
// a profile's grid, search and post authors.

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func healthUsernames(rows []map[string]interface{}, key string) []string {
	var out []string
	for _, r := range rows {
		s, _ := r[key].(string)
		out = append(out, s)
	}
	return out
}

// followersPage opens a profile whose header count opens the dialog; the
// dialog yields round1 then round2 (overlapping), then nothing new.
func followersPage(t *testing.T) *healthPage {
	r1 := healthFixture(t, "lists", "followers_round1")
	r2 := healthFixture(t, "lists", "followers_round2")
	return newHealthPage(t).scriptOpen().
		onValue(hsHeader, true).
		onValue(hsPrivate, false).
		onValue(hsCountLink, true).
		onValue(hsDialogN, 4).
		onSeq(hsDialogRow, r1, r2).
		onValue(hsDialogScr, true)
}

func TestHealthFetchFollowersDedupesAcrossScrolls(t *testing.T) {
	healthFast(t)
	p := followersPage(t)
	rows, err := New().FetchFollowersList(context.Background(), p, "https://www.instagram.com/synth.ada/", "FOLLOWING_FETCH", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := healthUsernames(rows, "username"); !reflect.DeepEqual(got, []string{"f.one", "f.two", "f.three", "f.four"}) {
		t.Errorf("usernames = %v", got)
	}
	first := rows[0]
	want := map[string]interface{}{
		"platform": "INSTAGRAM", "url": "https://www.instagram.com/f.one/", "profile_url": "https://www.instagram.com/f.one/",
		"full_name": "F One", "image_url": "https://cdn.example/1.jpg", "is_verified": true,
		"source": "following", "source_profile": "synth.ada",
	}
	for k, v := range want {
		if first[k] != v {
			t.Errorf("%s = %#v, want %#v", k, first[k], v)
		}
	}
	// Two productive rounds, then three stale ones end the loop.
	if got := p.count(hsDialogRow); got != 5 {
		t.Errorf("dialog read %d times, want 5 (stops after 3 rounds without growth)", got)
	}
	if p.clickCount() != 1 {
		t.Errorf("clicks = %d, want 1 (the header count)", p.clickCount())
	}
}

func TestHealthFetchFollowersStopsAtMax(t *testing.T) {
	healthFast(t)
	p := followersPage(t)
	rows, err := New().FetchFollowersList(context.Background(), p, "synth.ada", "followers", 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := healthUsernames(rows, "username"); !reflect.DeepEqual(got, []string{"f.one", "f.two", "f.three"}) {
		t.Errorf("usernames = %v, want the first 3", got)
	}
	if rows[0]["source"] != "followers" {
		t.Errorf("source = %v", rows[0]["source"])
	}
	if got := p.count(hsDialogRow); got != 2 {
		t.Errorf("dialog read %d times, want 2", got)
	}
}

func TestHealthFetchFollowersFallsBackToRoute(t *testing.T) {
	healthFast(t)
	// The header count is not clickable; the /followers/ route opens it.
	p := newHealthPage(t).scriptOpen().
		onValue(hsHeader, true).
		onValue(hsPrivate, false).
		onValue(hsCountLink, false).
		on(hsDialogN, func(c healthCall) interface{} {
			if strings.HasSuffix(c.URL, "/followers/") {
				return 2
			}
			return 0
		}).
		onValue(hsDialogRow, healthFixture(t, "lists", "followers_round1")).
		onValue(hsDialogScr, true)
	rows, err := New().FetchFollowersList(context.Background(), p, "", "https://www.instagram.com/synth.ada/", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Errorf("rows = %d, want 2", len(rows))
	}
	navs := p.navigations()
	if len(navs) != 2 || navs[1] != "https://www.instagram.com/synth.ada/followers/" {
		t.Errorf("navigations = %v", navs)
	}
}

func TestHealthFetchFollowersErrors(t *testing.T) {
	healthFast(t)
	b := New()
	ctx := context.Background()

	for _, prof := range []string{"", "followers", "FOLLOWING_FETCH"} {
		if _, err := b.FetchFollowersList(ctx, newHealthPage(t), prof, "followers", 5); err == nil || !strings.Contains(err.Error(), "no profile given") {
			t.Errorf("profile %q: err = %v", prof, err)
		}
	}
	if _, err := b.FetchFollowersList(ctx, newHealthPage(t), "a b c", "followers", 5); err == nil || !strings.Contains(err.Error(), "could not determine a username") {
		t.Errorf("bad profile: err = %v", err)
	}

	p := newHealthPage(t).scriptOpen().onValue(hsHeader, true).onValue(hsPrivate, true)
	if _, err := b.FetchFollowersList(ctx, p, "secret.one", "following", 5); err == nil || !strings.Contains(err.Error(), "private") {
		t.Errorf("private: err = %v", err)
	}

	p = newHealthPage(t).scriptOpen().onValue(hsHeader, false)
	if _, err := b.FetchFollowersList(ctx, p, "slow.one", "", 5); err == nil || !strings.Contains(err.Error(), "did not render") {
		t.Errorf("no header: err = %v", err)
	}

	p = newHealthPage(t).scriptOpen().onValue(hsHeader, true).onValue(hsPrivate, false).
		onValue(hsCountLink, false).onValue(hsDialogN, 0)
	if _, err := b.FetchFollowersList(ctx, p, "closed.one", "", 5); err == nil || !strings.Contains(err.Error(), "dialog did not open") {
		t.Errorf("dialog never opens: err = %v", err)
	}

	p = newHealthPage(t).scriptOpen().onValue(hsHeader, true).onValue(hsPrivate, false).
		onValue(hsCountLink, true).onValue(hsDialogN, 3).onValue(hsDialogRow, errors.New("script threw"))
	if _, err := b.FetchFollowersList(ctx, p, "broken.one", "", 5); err == nil || !strings.Contains(err.Error(), "read followers dialog") {
		t.Errorf("read failure: err = %v", err)
	}

	p = newHealthPage(t).onValue(hsLogin, true)
	if _, err := b.FetchFollowersList(ctx, p, "x.y", "", 5); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("login wall: err = %v", err)
	}
}

func TestHealthListUserPostsDedupesAndCaps(t *testing.T) {
	healthFast(t)
	p := newHealthPage(t).scriptOpen().
		onValue(hsHeader, true).
		onValue(hsGridState, map[string]interface{}{"private": false, "posts": 2}).
		onSeq(hsGrid, healthFixture(t, "lists", "grid_round1"), healthFixture(t, "lists", "grid_round2")).
		onValue(hsGridScr, true)
	rows, err := New().ListUserPosts(context.Background(), p, "@synth.ada", 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := healthUsernames(rows, "shortcode"); !reflect.DeepEqual(got, []string{"AAA", "BBB", "CCC"}) {
		t.Errorf("shortcodes = %v", got)
	}
	if rows[0]["author"] != "synth.ada" || rows[0]["author_url"] != "https://www.instagram.com/synth.ada/" {
		t.Errorf("default author not applied: %v", rows[0])
	}
	if rows[1]["author"] != "other.user" || rows[1]["type"] != "reel" {
		t.Errorf("own author/kind not kept: %v", rows[1])
	}
	if rows[0]["thumbnail_src"] != "https://cdn.example/a.jpg" || rows[0]["alt_text"] != "Photo A" {
		t.Errorf("thumbnail/alt = %v", rows[0])
	}
}

func TestHealthListUserPostsStopsWhenGridStopsGrowing(t *testing.T) {
	healthFast(t)
	p := newHealthPage(t).scriptOpen().
		onValue(hsHeader, true).
		onValue(hsGridState, map[string]interface{}{"private": false, "posts": 2}).
		onValue(hsGrid, healthFixture(t, "lists", "grid_round1")).
		onValue(hsGridScr, true)
	rows, err := New().ListUserPosts(context.Background(), p, "synth.ada", 0) // default 20
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Errorf("rows = %d, want 2", len(rows))
	}
	if got := p.count(hsGrid); got != 4 {
		t.Errorf("grid read %d times, want 4 (1 productive + 3 stale)", got)
	}
}

func TestHealthListUserPostsErrors(t *testing.T) {
	healthFast(t)
	p := newHealthPage(t).scriptOpen().
		onValue(hsHeader, true).
		onValue(hsGridState, map[string]interface{}{"private": true, "posts": 0})
	if _, err := New().ListUserPosts(context.Background(), p, "secret.one", 5); err == nil || !strings.Contains(err.Error(), "private") {
		t.Errorf("private: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsHeader, false)
	if _, err := New().ListUserPosts(context.Background(), p, "slow.one", 5); err == nil || !strings.Contains(err.Error(), "did not render") {
		t.Errorf("no header: err = %v", err)
	}
	// A grid read failure returns the error with what was collected.
	p = newHealthPage(t).scriptOpen().
		onValue(hsHeader, true).
		onValue(hsGridState, map[string]interface{}{"private": false, "posts": 1}).
		onSeq(hsGrid, healthFixture(t, "lists", "grid_round1"), errors.New("detached")).
		onValue(hsGridScr, true)
	rows, err := New().ListUserPosts(context.Background(), p, "synth.ada", 10)
	if err == nil || len(rows) != 2 {
		t.Errorf("partial = %d rows, %v; want 2 rows and the error", len(rows), err)
	}
}

func TestHealthSearchPostsTagThenKeyword(t *testing.T) {
	healthFast(t)
	// The hashtag page is empty; keyword search has results.
	p := newHealthPage(t).scriptOpen().
		onValue(hsSearchN, 0).
		on(hsGrid, func(c healthCall) interface{} {
			if strings.Contains(c.URL, "/explore/tags/") {
				return []interface{}{}
			}
			return healthFixture(t, "lists", "grid_round1")
		}).
		onValue(hsGridScr, true)
	rows, err := New().SearchPosts(context.Background(), p, "#sunset", 5)
	if err != nil {
		t.Fatal(err)
	}
	navs := p.navigations()
	if len(navs) != 2 || navs[0] != "https://www.instagram.com/explore/tags/sunset/" ||
		navs[1] != "https://www.instagram.com/explore/search/keyword/?q=%23sunset" {
		t.Errorf("navigations = %v", navs)
	}
	if len(rows) != 2 || rows[0]["keyword"] != "#sunset" {
		t.Errorf("rows = %v", rows)
	}
	if _, has := rows[0]["author"]; has {
		t.Errorf("a search result without author must not get one: %v", rows[0])
	}
}

func TestHealthSearchPostsMultiWordAndEmpty(t *testing.T) {
	healthFast(t)
	p := newHealthPage(t).scriptOpen().onValue(hsSearchN, 0).onValue(hsGrid, []interface{}{}).onValue(hsGridScr, true)
	rows, err := New().SearchPosts(context.Background(), p, "red shoes", 0)
	if err != nil {
		t.Fatal(err)
	}
	if rows == nil || len(rows) != 0 {
		t.Errorf("rows = %#v, want an empty (non-nil) list", rows)
	}
	if navs := p.navigations(); len(navs) != 1 || !strings.Contains(navs[0], "keyword/?q=red+shoes") {
		t.Errorf("navigations = %v (multi-word skips the tag page)", navs)
	}
	if _, err := New().SearchPosts(context.Background(), p, "  # ", 5); err == nil {
		t.Error("empty keyword: want error")
	}
}

func TestHealthSearchPostsErrors(t *testing.T) {
	healthFast(t)
	p := newHealthPage(t).onValue(hsLogin, true)
	if _, err := New().SearchPosts(context.Background(), p, "sun", 5); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("login wall: err = %v", err)
	}
	if n := len(p.navigations()); n != 1 {
		t.Errorf("navigations = %d, want 1 (a login wall stops the search)", n)
	}
	p = newHealthPage(t)
	p.navErr = errors.New("offline")
	if _, err := New().SearchPosts(context.Background(), p, "sun", 5); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Errorf("navigation failure: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsSearchN, 1).onValue(hsGrid, errors.New("boom"))
	if _, err := New().SearchPosts(context.Background(), p, "sun", 5); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("grid failure: err = %v", err)
	}
}

func TestHealthFindPostAuthors(t *testing.T) {
	healthFast(t)
	grid := []interface{}{
		map[string]interface{}{"url": "https://www.instagram.com/p/P1/", "shortcode": "P1", "kind": "p", "author": "synth.ada"},
		map[string]interface{}{"url": "https://www.instagram.com/p/P2/", "shortcode": "P2", "kind": "p", "author": "SYNTH.ADA"},
		map[string]interface{}{"url": "https://www.instagram.com/p/P3/", "shortcode": "P3", "kind": "p", "author": ""},
		map[string]interface{}{"url": "https://www.instagram.com/p/P4/", "shortcode": "P4", "kind": "p", "author": ""},
	}
	post := healthFixture(t, "lists", "post").(map[string]interface{})
	p := newHealthPage(t).scriptOpen().
		onValue(hsSearchN, 4).onValue(hsGrid, grid).onValue(hsGridScr, true).
		onValue(hsGone, false).
		on(hsPostData, func(c healthCall) interface{} {
			if strings.Contains(c.URL, "/P3/") {
				m := map[string]interface{}{}
				for k, v := range post {
					m[k] = v
				}
				m["author_username"] = "synth.bob"
				return m
			}
			return healthFixture(t, "lists", "post_unrendered") // P4: author never readable
		}).
		on(hsStore, func(c healthCall) interface{} {
			if strings.Contains(c.URL, "synth.ada") {
				return healthFixture(t, "profile", "store")
			}
			return map[string]interface{}{"state": "wait"}
		}).
		onValue(hsFetchAPI, map[string]interface{}{"error": "HTTP 404"}).
		onValue(hsDOM, healthFixture(t, "profile", "dom_unavailable"))
	rows, err := New().FindPostAuthors(context.Background(), p, "sky", 5)
	if err != nil {
		t.Fatal(err)
	}
	if got := healthUsernames(rows, "username"); !reflect.DeepEqual(got, []string{"synth.ada", "synth.bob"}) {
		t.Fatalf("authors = %v (deduped case-insensitively; unreadable post skipped)", got)
	}
	if rows[0]["source_post"] != "https://www.instagram.com/p/P1/" || rows[0]["keyword"] != "sky" {
		t.Errorf("row 0 = %v", rows[0])
	}
	if rows[1]["profile_error"] == nil || rows[1]["source_post"] != "https://www.instagram.com/p/P3/" {
		t.Errorf("unreadable profile must be kept with profile_error: %v", rows[1])
	}
}

func TestHealthFindPostAuthorsMaxAndErrors(t *testing.T) {
	healthFast(t)
	grid := []interface{}{
		map[string]interface{}{"url": "https://www.instagram.com/p/P1/", "shortcode": "P1", "kind": "p", "author": "synth.ada"},
		map[string]interface{}{"url": "https://www.instagram.com/p/P2/", "shortcode": "P2", "kind": "p", "author": "other.one"},
	}
	p := newHealthPage(t).scriptOpen().
		onValue(hsSearchN, 2).onValue(hsGrid, grid).onValue(hsGridScr, true).
		onValue(hsStore, healthFixture(t, "profile", "store"))
	rows, err := New().FindPostAuthors(context.Background(), p, "sky", 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("= %d rows, %v; want 1", len(rows), err)
	}

	p = newHealthPage(t).onValue(hsLogin, true)
	if _, err := New().FindPostAuthors(context.Background(), p, "sky", 0); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("login wall: err = %v", err)
	}

	// The profile page hits a login wall mid-way: return what was found.
	p = newHealthPage(t).
		on(hsLogin, func(c healthCall) interface{} { return strings.Contains(c.URL, "/other.one/") }).
		onValue(hsNotNow, false).onValue(hsUnmark, true).
		onValue(hsSearchN, 2).onValue(hsGrid, grid).onValue(hsGridScr, true).
		onValue(hsStore, healthFixture(t, "profile", "store"))
	rows, err = New().FindPostAuthors(context.Background(), p, "sky", 5)
	if !errors.Is(err, ErrNotLoggedIn) || len(rows) != 1 {
		t.Errorf("= %d rows, %v; want 1 row and ErrNotLoggedIn", len(rows), err)
	}
}

func TestHealthListMethodsThroughGetMethodByName(t *testing.T) {
	healthFast(t)
	b := New()
	for _, name := range []string{"fetch_followers_list", "list_user_posts", "search_posts", "find_post_authors", "list_post_comments", "scrape_post_data"} {
		fn, ok := b.GetMethodByName(name)
		if !ok {
			t.Fatalf("%s missing", name)
		}
		if _, err := fn(context.Background()); err == nil {
			t.Errorf("%s without a page: want error", name)
		}
	}
	fn, _ := b.GetMethodByName("list_user_posts")
	p := newHealthPage(t).scriptOpen().
		onValue(hsHeader, true).
		onValue(hsGridState, map[string]interface{}{"private": false, "posts": 2}).
		onValue(hsGrid, healthFixture(t, "lists", "grid_round1")).
		onValue(hsGridScr, true)
	res, err := fn(context.Background(), p, "synth.ada", "1")
	if err != nil {
		t.Fatal(err)
	}
	if l, _ := res.([]interface{}); len(l) != 1 {
		t.Errorf("list_user_posts = %#v, want 1 row", res)
	}
	fn, _ = b.GetMethodByName("search_posts")
	p = newHealthPage(t).scriptOpen().onValue(hsSearchN, 0).onValue(hsGrid, []interface{}{}).onValue(hsGridScr, true)
	if res, err := fn(context.Background(), p, "a b", ""); err != nil {
		t.Fatal(err)
	} else if l, ok := res.([]interface{}); !ok || len(l) != 0 {
		t.Errorf("search_posts empty = %#v", res)
	}
	fn, _ = b.GetMethodByName("fetch_followers_list")
	if _, err := fn(context.Background(), newHealthPage(t), "", "", ""); err == nil {
		t.Error("fetch_followers_list without profile: want error")
	}
}
