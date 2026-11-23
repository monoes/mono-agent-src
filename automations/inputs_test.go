package automations

import (
	"encoding/json"
	"testing"
)

type inputDecl struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Default any      `json:"default"`
	Max     *float64 `json:"max"`
}

// declared returns the object-form inputs of an action and the names of its
// legacy string-form inputs.
func declared(t *testing.T, action string) (map[string]inputDecl, []string) {
	t.Helper()
	b, err := files.ReadFile(action)
	if err != nil {
		t.Fatal(err)
	}
	var a struct {
		Inputs struct {
			Required []json.RawMessage `json:"required"`
			Optional []json.RawMessage `json:"optional"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(b, &a); err != nil {
		t.Fatal(err)
	}
	out := map[string]inputDecl{}
	var legacy []string
	for _, raw := range append(a.Inputs.Required, a.Inputs.Optional...) {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			legacy = append(legacy, s)
			continue
		}
		var d inputDecl
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		out[d.Name] = d
	}
	return out, legacy
}

// R26-03/04/05: a count or delay input that bounds how much an action does
// has a default above 0 (0 would mean unbounded) and a max, so a value like
// 100000 does not pass the form.
func TestCountInputsAreBounded(t *testing.T) {
	cases := []struct {
		action, input string
		maxAtMost     float64
	}{
		{"instagram/actions/engage_with_posts.json", "maxContentCount", 100},
		{"instagram/actions/engage_user_posts.json", "maxContentCount", 100},
		{"tiktok/actions/engage_with_posts.json", "maxContentCount", 100},
		{"instagram/actions/watch_stories.json", "maxStoriesPerUser", 500},
		{"instagram/actions/extract_post_data.json", "maxComments", 500},
		{"hackernews/actions/list_comments.json", "maxComments", 500},
		{"instagram/actions/find_by_keyword.json", "maxResultsCount", 500},
		{"linkedin/actions/find_by_keyword.json", "maxResultsCount", 500},
		{"tiktok/actions/auto_reply_dms.json", "maxReplies", 100},
		{"tiktok/actions/auto_reply_dms.json", "delayBetweenReplies", 60},
		{"tiktok/actions/send_dms.json", "delayBetweenMessages", 60},
		{"tiktok/actions/engage_with_posts.json", "delayBetweenActions", 60},
	}
	for _, c := range cases {
		in, _ := declared(t, c.action)
		d, ok := in[c.input]
		switch {
		case !ok:
			t.Errorf("%s: %s is not declared as an object input", c.action, c.input)
		case d.Max == nil || *d.Max > c.maxAtMost:
			t.Errorf("%s: %s max = %v, want <= %v", c.action, c.input, d.Max, c.maxAtMost)
		case func() bool { f, ok := d.Default.(float64); return !ok || f <= 0 }():
			t.Errorf("%s: %s default = %v, want > 0", c.action, c.input, d.Default)
		}
	}
}

// R26-05: the TikTok actions declare every input with a type like the other
// platforms, not as a bare name that carries no type, default or bound.
func TestTikTokInputsAreObjects(t *testing.T) {
	for _, a := range []string{"auto_reply_dms", "send_dms", "publish_post", "engage_with_posts"} {
		_, legacy := declared(t, "tiktok/actions/"+a+".json")
		if len(legacy) > 0 {
			t.Errorf("tiktok/%s: legacy string inputs %v", a, legacy)
		}
	}
}
