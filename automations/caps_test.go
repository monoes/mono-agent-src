package automations

import (
	"encoding/json"
	"fmt"
	"testing"
)

// R26-01/R14-D7: a live write or message action declares its per-run limit as
// an input with a default and a max, and its loop is capped by it, so no run
// is unbounded even when the caller names no limit. Likes, follows, shares:
// default 10, max 50. Comments, DMs, replies: default 5, max 20. (It is an
// optional input, not a required one: TestBuiltinForms_RequiredInputsAreRequiredFields
// in internal/nodes rejects a required input that only has a default.)
func TestLiveWriteActionsAreCapped(t *testing.T) {
	const (
		writeDefault, writeMax = 10, 50
		msgDefault, msgMax     = 5, 20
	)
	cases := []struct {
		action, input string
		def, max      float64
	}{
		{"linkedin/actions/like_posts.json", "maxLikesPerRun", writeDefault, writeMax},
		{"linkedin/actions/like_comments.json", "maxLikesPerRun", writeDefault, writeMax},
		{"linkedin/actions/comment_on_posts.json", "maxCommentsPerRun", msgDefault, msgMax},
		{"linkedin/actions/send_dms.json", "maxMessagesPerRun", msgDefault, msgMax},
		{"tiktok/actions/follow_user.json", "maxFollowsPerRun", writeDefault, writeMax},
		{"tiktok/actions/like_video.json", "maxLikesPerRun", writeDefault, writeMax},
		{"tiktok/actions/like_comment.json", "maxLikesPerRun", writeDefault, writeMax},
		{"tiktok/actions/share_video.json", "maxSharesPerRun", writeDefault, writeMax},
		{"tiktok/actions/duet_video.json", "maxDuetsPerRun", writeDefault, writeMax},
		{"tiktok/actions/stitch_video.json", "maxStitchesPerRun", writeDefault, writeMax},
		{"tiktok/actions/comment_on_video.json", "maxCommentsPerRun", msgDefault, msgMax},
		{"tiktok/actions/send_dms.json", "maxMessagesPerRun", msgDefault, msgMax},
		{"instagram/actions/reply_to_comments.json", "maxRepliesPerRun", msgDefault, msgMax},
	}
	for _, c := range cases {
		b, err := files.ReadFile(c.action)
		if err != nil {
			t.Fatal(err)
		}
		var a struct {
			Inputs struct {
				Optional []json.RawMessage `json:"optional"`
			} `json:"inputs"`
			Loops []struct {
				MaxItems string `json:"maxItems"`
			} `json:"loops"`
		}
		if err := json.Unmarshal(b, &a); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, raw := range a.Inputs.Optional {
			var d inputDecl
			if json.Unmarshal(raw, &d) != nil || d.Name != c.input {
				continue
			}
			found = true
			if d.Max == nil || *d.Max != c.max {
				t.Errorf("%s: %s max = %v, want %v", c.action, c.input, d.Max, c.max)
			}
			if f, ok := d.Default.(float64); !ok || f != c.def {
				t.Errorf("%s: %s default = %v, want %v", c.action, c.input, d.Default, c.def)
			}
		}
		if !found {
			t.Errorf("%s: %s is not declared as an optional input", c.action, c.input)
		}
		if len(a.Loops) == 0 {
			t.Errorf("%s: no loop", c.action)
		}
		for _, l := range a.Loops {
			if l.MaxItems != fmt.Sprintf("{{%s or %d}}", c.input, int(c.def)) {
				t.Errorf("%s: loop maxItems = %q, want {{%s or %d}}", c.action, l.MaxItems, c.input, int(c.def))
			}
		}
	}
}

// R26-02: instagram auto_reply_dms bounds a run by stopAfterReplies (default
// 20, max 100, the loop's maxItems) and no longer declares the pollInterval
// that no step read.
func TestInstagramAutoReplyIsBounded(t *testing.T) {
	in, _ := declared(t, "instagram/actions/auto_reply_dms.json")
	d, ok := in["stopAfterReplies"]
	if !ok || d.Max == nil || *d.Max != 100 {
		t.Fatalf("stopAfterReplies = %+v, want max 100", d)
	}
	if f, ok := d.Default.(float64); !ok || f != 20 {
		t.Errorf("stopAfterReplies default = %v, want 20", d.Default)
	}
	if _, ok := in["pollInterval"]; ok {
		t.Error("pollInterval is declared but no step reads it")
	}
	b, _ := files.ReadFile("instagram/actions/auto_reply_dms.json")
	var a struct {
		Loops []struct {
			MaxItems string `json:"maxItems"`
		} `json:"loops"`
	}
	if err := json.Unmarshal(b, &a); err != nil || len(a.Loops) != 1 || a.Loops[0].MaxItems != "{{stopAfterReplies or 20}}" {
		t.Errorf("loops = %+v (%v), want maxItems {{stopAfterReplies or 20}}", a.Loops, err)
	}
}
