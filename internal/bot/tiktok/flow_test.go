//go:build !nosocial

package tiktok

// JSON-flow tests: the shipped action definitions (data/actions/tiktok, via
// the embedded loader) run through internal/action's executor against the
// fixture browser, exactly as a workflow node runs them.

import (
	"context"
	"fmt"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/bot/bottest"
)

type flow struct {
	typ     string
	message string
	keyword string
	params  map[string]interface{}
	items   []interface{}
}

// runFlow executes the action and returns its result and error.
func runFlow(t *testing.T, p *bottest.Page, b *TikTokBot, f flow) (*action.ExecutionResult, error) {
	t.Helper()
	testhome.SetHome(t, t.TempDir()) // the loader's user-template fallback must not see a real ~/.monoagent
	params := map[string]interface{}{
		"delayBetweenLikes": 0.01, "delayBetweenFollows": 0.01, "delayBetweenComments": 0.01,
		"delayBetweenMessages": 0.01, "delayBetweenReplies": 0.01, "delayBetweenActions": 0.01,
		"delayBetweenProfiles": 0.01, "delayBetweenVideos": 0.01,
	}
	for k, v := range f.params {
		params[k] = v
	}
	sa := &action.StorageAction{
		ID: "test-" + f.typ, Type: f.typ, TargetPlatform: "TIKTOK",
		ContentMessage: f.message, Keywords: f.keyword, Params: params,
	}
	ae := action.NewActionExecutor(context.Background(), p, nil, nil, nil, b, zerolog.Nop())
	if f.items != nil {
		ae.SetVariable("selectedListItems", f.items)
	}
	res, err := ae.Execute(sa)
	if err == nil {
		assertOutputsDeclared(t, f.typ, res)
	}
	return res, err
}

func target(u string) map[string]interface{} {
	return map[string]interface{}{"url": u, "href": u, "username": u}
}

func statuses(res *action.ExecutionResult) []string {
	var out []string
	for _, it := range res.ExtractedItems {
		if s, ok := it["status"].(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func failedSteps(res *action.ExecutionResult) []string {
	var out []string
	if res == nil {
		return nil
	}
	for _, f := range res.FailedItems {
		out = append(out, fmt.Sprintf("%s: %v", f.StepID, f.Error))
	}
	return out
}

func TestFlowSendDMs(t *testing.T) {
	p := newPage(t)
	res, err := runFlow(t, p, &TikTokBot{}, flow{typ: "send_dms", message: "Hi from the flow",
		items: []interface{}{target(profileURL("fake_creator"))}})
	if err != nil {
		t.Fatalf("err = %v (failed: %v)", err, failedSteps(res))
	}
	if s := statuses(res); strings.Join(s, ",") != "sent" {
		t.Fatalf("statuses = %v", s)
	}
	wantEvents(t, p, "send:fake_creator:Hi from the flow")
}

// A wrong chat, a silent send failure and a closed inbox are each reported
// as failed items and fail the action; the one good recipient still gets
// the message.
func TestFlowSendDMsReportsFailures(t *testing.T) {
	p := newPage(t)
	res, err := runFlow(t, p, &TikTokBot{}, flow{typ: "send_dms", message: "Batch hello",
		items: []interface{}{
			target(profileURL("fake_wrongchat")),
			target(profileURL("fake_mute")),
			target(profileURL("fake_nodm")),
			target(profileURL("fake_creator")),
		}})
	if err == nil {
		t.Fatal("want an error for the failed recipients")
	}
	fs := failedSteps(res)
	if len(fs) != 3 {
		t.Fatalf("failed = %v, want 3", fs)
	}
	for i, want := range []string{"not @fake_wrongchat", "could not be verified", "Message button not found"} {
		if !strings.Contains(fs[i], want) {
			t.Fatalf("failure %d = %q, want %q", i, fs[i], want)
		}
	}
	if s := statuses(res); strings.Join(s, ",") != "sent" {
		t.Fatalf("statuses = %v", s)
	}
}

func TestFlowAutoReplyDMs(t *testing.T) {
	p := newPage(t)
	res, err := runFlow(t, p, &TikTokBot{}, flow{typ: "auto_reply_dms", message: "Auto reply text",
		params: map[string]interface{}{"startDate": "2026-01-01", "endDate": "2026-12-31", "pollInterval": 60}})
	// Unread: Fake Ana (unique) and two conversations both named Fake Cy,
	// which must fail instead of replying to either.
	if err == nil {
		t.Fatal("want an error for the ambiguous conversations")
	}
	fs := failedSteps(res)
	if len(fs) != 2 || !strings.Contains(fs[0], "2 conversations named \"Fake Cy\"") {
		t.Fatalf("failed = %v", fs)
	}
	var sent []string
	for _, it := range res.ExtractedItems {
		if it["status"] == "sent" {
			sent = append(sent, fmt.Sprint(it["conversation"]))
		}
	}
	if strings.Join(sent, ",") != "Fake Ana" {
		t.Fatalf("replied to %v", sent)
	}
}

func TestFlowEngageWithPostsLikesAndComments(t *testing.T) {
	p := newPage(t)
	res, err := runFlow(t, p, &TikTokBot{}, flow{typ: "engage_with_posts", message: "Great clip!",
		params: map[string]interface{}{"maxContentCount": 2, "searches": []interface{}{map[string]interface{}{"keyword": "cats"}}}})
	if err != nil {
		t.Fatalf("err = %v (failed: %v)", err, failedSteps(res))
	}
	var liked, commented []string
	for _, it := range res.ExtractedItems {
		switch it["status"] {
		case "liked":
			liked = append(liked, fmt.Sprint(it["videoURL"]))
		case "commented":
			commented = append(commented, fmt.Sprint(it["videoURL"]))
		}
	}
	want := []string{
		"https://www.tiktok.com/@fake_maker_0/video/7300000000000000001",
		"https://www.tiktok.com/@fake_maker_1/video/7300000000000000002",
	}
	if strings.Join(liked, ",") != strings.Join(want, ",") || strings.Join(commented, ",") != strings.Join(want, ",") {
		t.Fatalf("liked %v, commented %v; want both %v", liked, commented, want)
	}
	// comment_on_video reloads the video, so only its own event is on the page.
	wantEvents(t, p, "comment-post:Great clip!")
}

func TestFlowEngageWithPostsLikeOnly(t *testing.T) {
	p := newPage(t)
	res, err := runFlow(t, p, &TikTokBot{}, flow{typ: "engage_with_posts",
		params: map[string]interface{}{"maxContentCount": 1, "searches": []interface{}{map[string]interface{}{"keyword": "dogs"}}}})
	if err != nil {
		t.Fatalf("err = %v (failed: %v)", err, failedSteps(res))
	}
	if s := statuses(res); strings.Join(s, ",") != "liked" {
		t.Fatalf("statuses = %v", s)
	}
	wantEvents(t, p, "video-like-click")
}

func TestFlowPublishPost(t *testing.T) {
	p := newPage(t)
	media := filepath.Join(t.TempDir(), "synthetic_clip.mp4")
	if err := os.WriteFile(media, []byte("not a video"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := runFlow(t, p, &TikTokBot{}, flow{typ: "publish_post", message: "Flow caption",
		params: map[string]interface{}{"media": media}})
	if err != nil {
		t.Fatalf("err = %v (failed: %v)", err, failedSteps(res))
	}
	if s := statuses(res); strings.Join(s, ",") != "published" {
		t.Fatalf("statuses = %v", s)
	}
	if got := evalString(t, p, `document.body.getAttribute('data-posted')`); got != "posted:Flow caption" {
		t.Fatalf("posted %q", got)
	}
}

func TestFlowLikeVideo(t *testing.T) {
	p := newPage(t)
	res, err := runFlow(t, p, &TikTokBot{}, flow{typ: "like_video",
		items: []interface{}{target(vidPlain), vidLiked, target(vidLikeBroken)}})
	if err == nil {
		t.Fatal("want an error for the unconfirmed like")
	}
	if s := statuses(res); strings.Join(s, ",") != "liked,already_liked" {
		t.Fatalf("statuses = %v", s)
	}
	if fs := failedSteps(res); len(fs) != 1 || !strings.Contains(fs[0], "not confirmed") {
		t.Fatalf("failed = %v", fs)
	}
}

func TestFlowFollowUser(t *testing.T) {
	p := newPage(t)
	res, err := runFlow(t, p, &TikTokBot{}, flow{typ: "follow_user",
		items: []interface{}{target(profileURL("fake_creator")), target(profileURL("fake_friends"))}})
	if err != nil {
		t.Fatalf("err = %v (failed: %v)", err, failedSteps(res))
	}
	if s := statuses(res); strings.Join(s, ",") != "followed,already_following" {
		t.Fatalf("statuses = %v", s)
	}
}

// list_video_comments rows feed like_comment directly.
func TestFlowListCommentsThenLikeComment(t *testing.T) {
	p := newPage(t)
	res, err := runFlow(t, p, &TikTokBot{}, flow{typ: "list_video_comments",
		params: map[string]interface{}{"maxComments": 3}, items: []interface{}{target(vidPlain)}})
	if err != nil {
		t.Fatalf("err = %v (failed: %v)", err, failedSteps(res))
	}
	var ana map[string]interface{}
	for _, it := range res.ExtractedItems {
		if it["username"] == "fake_ana" {
			ana = it
		}
	}
	if ana == nil {
		t.Fatalf("no row for fake_ana in %v", res.ExtractedItems)
	}
	res, err = runFlow(t, p, &TikTokBot{}, flow{typ: "like_comment", items: []interface{}{ana}})
	if err != nil {
		t.Fatalf("err = %v (failed: %v)", err, failedSteps(res))
	}
	if s := statuses(res); strings.Join(s, ",") != "liked" {
		t.Fatalf("statuses = %v", s)
	}
	wantEvents(t, p, "comment-like-click:fake_ana")
}

func TestFlowCommentOnVideo(t *testing.T) {
	p := newPage(t)
	res, err := runFlow(t, p, &TikTokBot{}, flow{typ: "comment_on_video", message: "Flow comment",
		items: []interface{}{target(vidPlain), target(vidCommentBad)}})
	if err == nil {
		t.Fatal("want an error for the failed comment")
	}
	if s := statuses(res); strings.Join(s, ",") != "commented" {
		t.Fatalf("statuses = %v", s)
	}
	if fs := failedSteps(res); len(fs) != 1 || !strings.Contains(fs[0], "not confirmed") {
		t.Fatalf("failed = %v", fs)
	}
}

func TestFlowScrapeProfileInfo(t *testing.T) {
	p := newPage(t)
	res, err := runFlow(t, p, &TikTokBot{}, flow{typ: "scrape_profile_info",
		items: []interface{}{target(profileURL("fake_creator")), target(vidPlain)}})
	if err == nil {
		t.Fatal("want an error for the page without a profile header")
	}
	if len(res.ExtractedItems) != 1 || res.ExtractedItems[0]["full_name"] != "Fake Creator" || res.ExtractedItems[0]["follower_count"] != "1234" {
		t.Fatalf("items = %v", res.ExtractedItems)
	}
	if fs := failedSteps(res); len(fs) != 1 || !strings.Contains(fs[0], "no profile header") {
		t.Fatalf("failed = %v", fs)
	}
}

func TestFlowFindByKeyword(t *testing.T) {
	p := newPage(t)
	res, err := runFlow(t, p, &TikTokBot{}, flow{typ: "find_by_keyword", keyword: "cats",
		params: map[string]interface{}{"maxResultsCount": 5}})
	if err != nil {
		t.Fatalf("err = %v (failed: %v)", err, failedSteps(res))
	}
	if len(res.ExtractedItems) != 5 || res.ExtractedItems[0]["author"] != "fake_maker_0" {
		t.Fatalf("items = %v", res.ExtractedItems)
	}
}

func TestFlowExportFollowers(t *testing.T) {
	p := newPage(t)
	res, err := runFlow(t, p, &TikTokBot{}, flow{typ: "export_followers",
		params: map[string]interface{}{"sourceType": "FOLLOWING_FETCH", "maxResultsCount": 4, "profileUrl": profileURL("fake_creator")}})
	if err != nil {
		t.Fatalf("err = %v (failed: %v)", err, failedSteps(res))
	}
	if len(res.ExtractedItems) != 4 || res.ExtractedItems[3]["username"] != "fake_following_4" {
		t.Fatalf("items = %v", res.ExtractedItems)
	}
}

func TestFlowShareStitchDuet(t *testing.T) {
	for _, c := range []struct{ typ, status string }{
		{"share_video", "link_copied"}, {"stitch_video", "opened_stitch_editor"}, {"duet_video", "opened_duet_editor"},
	} {
		t.Run(c.typ, func(t *testing.T) {
			p := newPage(t)
			res, err := runFlow(t, p, &TikTokBot{}, flow{typ: c.typ, items: []interface{}{target(vidPlain), target(vidRemixOff)}})
			if c.typ == "share_video" {
				if err != nil {
					t.Fatalf("err = %v (failed: %v)", err, failedSteps(res))
				}
				if s := statuses(res); strings.Join(s, ",") != "link_copied,link_copied" {
					t.Fatalf("statuses = %v", s)
				}
				return
			}
			if err == nil {
				t.Fatal("want an error for the disabled option")
			}
			if s := statuses(res); strings.Join(s, ",") != c.status {
				t.Fatalf("statuses = %v", s)
			}
		})
	}
}

func TestFlowListUserVideos(t *testing.T) {
	p := newPage(t)
	res, err := runFlow(t, p, &TikTokBot{}, flow{typ: "list_user_videos",
		params: map[string]interface{}{"maxVideos": 5}, items: []interface{}{target(profileURL("fake_creator"))}})
	if err != nil {
		t.Fatalf("err = %v (failed: %v)", err, failedSteps(res))
	}
	if len(res.ExtractedItems) != 5 {
		t.Fatalf("items = %v", res.ExtractedItems)
	}
}
