package peoplenodes

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/monoes/mono-agent/internal/bot/instagram"
	_ "github.com/monoes/mono-agent/internal/bot/linkedin"
	_ "github.com/monoes/mono-agent/internal/bot/tiktok"
	_ "github.com/monoes/mono-agent/internal/bot/x"
	"github.com/monoes/mono-agent/internal/nodes/nodeprofile"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/peoplereview"
	"github.com/monoes/mono-agent/internal/personitem"
	"github.com/monoes/mono-agent/internal/personphoto"
	"github.com/monoes/mono-agent/internal/util"
	"github.com/monoes/mono-agent/internal/workflow"
)

// globalPeopleDB is the SQLite DB used by PeopleSaveNode. Set at startup.
var globalPeopleDB *sql.DB

// SetGlobalPeopleDB wires the shared SQLite connection into all PeopleSaveNode instances.
func SetGlobalPeopleDB(db *sql.DB) {
	globalPeopleDB = db
}

// RegisterAll registers all people node types into the registry.
func RegisterAll(r *workflow.NodeTypeRegistry, db *sql.DB) {
	SetGlobalPeopleDB(db)
	r.Register("people.save", func() workflow.NodeExecutor { return &PeopleSaveNode{} })
	r.Register("people.sync_outlook_message", func() workflow.NodeExecutor { return &SyncOutlookMessageNode{} })
}

// PeopleSaveNode upserts input items into the people SQLite table.
// Type: "people.save"
//
// Each input item should have at least one of:
//   - profile_url / url  — the person's profile URL (used to extract username)
//   - full_name          — display name
//   - platform           — overridden by the node config's "platform" field if set
//
// Items without a resolvable username are skipped.
type PeopleSaveNode struct{}

func (n *PeopleSaveNode) Type() string { return "people.save" }

// PerItemConfigFields declares that "introduction", "category", and "job_title" can be template expressions evaluated per item.
func (n *PeopleSaveNode) PerItemConfigFields() []string {
	return []string{"introduction", "category", "job_title"}
}

func (n *PeopleSaveNode) Execute(
	ctx context.Context,
	input workflow.NodeInput,
	config map[string]interface{},
) ([]workflow.NodeOutput, error) {
	if globalPeopleDB == nil {
		return nil, fmt.Errorf("people.save: database not available (call SetGlobalPeopleDB at startup)")
	}

	// Config-level platform override.
	configPlatform, _ := config["platform"].(string)

	// The owning profile: the profile the run belongs to (the node's optional
	// profile_id must match it; a legacy "default" is ignored). No fallback.
	profileID, err := nodeprofile.Require(ctx, "people.save", config)
	if err != nil {
		return nil, err
	}

	tx, err := globalPeopleDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("people.save: begin tx: %w", err)
	}
	defer tx.Rollback() // no-op after Commit

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO people (id, platform_username, platform, full_name, image_url,
		        contact_details, website, content_count, follower_count,
		        following_count, introduction, is_verified, category, job_title,
		        headline, location, about, experience, education, profile_details,
		        profile_url, profile_id, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(platform_username, platform, profile_id)
		 DO UPDATE SET
		   full_name       = COALESCE(excluded.full_name,       people.full_name),
		   image_url       = COALESCE(excluded.image_url,       people.image_url),
		   profile_url     = COALESCE(excluded.profile_url,     people.profile_url),
		   website         = COALESCE(excluded.website,         people.website),
		   content_count   = COALESCE(excluded.content_count,   people.content_count),
		   follower_count  = COALESCE(excluded.follower_count,  people.follower_count),
		   following_count = COALESCE(excluded.following_count, people.following_count),
		   introduction    = COALESCE(excluded.introduction,    people.introduction),
		   is_verified     = COALESCE(excluded.is_verified,     people.is_verified),
		   category        = COALESCE(excluded.category,        people.category),
		   job_title       = COALESCE(excluded.job_title,       people.job_title),
		   headline        = COALESCE(excluded.headline,        people.headline),
		   location        = COALESCE(excluded.location,        people.location),
		   about           = COALESCE(excluded.about,           people.about),
		   experience      = COALESCE(excluded.experience,      people.experience),
		   education       = COALESCE(excluded.education,       people.education),
		   profile_details = `+personitem.DetailsMergeSQL+`,
		   updated_at      = excluded.updated_at`,
	)
	if err != nil {
		return nil, fmt.Errorf("people.save: prepare stmt: %w", err)
	}
	defer stmt.Close()

	now := time.Now().UTC()
	var savedItems []workflow.Item
	var saved []personphoto.Ref
	exprEngine := workflow.NewExpressionEngine()
	// A run the user did not start themselves (an AI started it, or wrote
	// or changed the workflow) never approves anyone for outreach: approving
	// lets the drafted DM go out (peoplereview.NotOperatorCategory).
	aiRun := operator.AIRun(ctx)

	for _, item := range input.Items {
		data := item.JSON

		exprCtx := workflow.ExpressionContext{
			JSON:        data,
			Node:        input.NodeOutputs,
			WorkflowID:  input.WorkflowID,
			ExecutionID: input.ExecutionID,
		}

		// Resolve platform (config override > URL inference > item field).
		platform := configPlatform
		if platform == "" {
			platform = platformFromURL(firstString(data, "profile_url", "url", "href", "author_url"))
		}
		if platform == "" {
			platform, _ = data["platform"].(string)
		}
		platformUpper := strings.ToUpper(platform)

		// The person the item is about: its own profile URL, or the author
		// of a post or comment. A URL that names no person (a post
		// permalink, a company page) saves nobody.
		lookup := data
		if _, ok := data["url"]; !ok {
			if href, ok := data["href"].(string); ok {
				lookup = map[string]interface{}{"url": href}
				for k, v := range data {
					lookup[k] = v
				}
			}
		}
		ref, ok := personitem.Resolve(platformUpper, lookup)
		if !ok {
			continue // Cannot save without a username.
		}
		username, profileURL := ref.Username, ref.ProfileURL
		prof := personitem.Profile{FullName: ref.FullName}
		if !ref.Author {
			prof = personitem.ProfileOf(data)
		}

		fullName := prof.FullName
		imageURL := prof.ImageURL
		website := prof.Website
		jobTitle := prof.JobTitle
		if jobTitle == "" {
			if jt, ok := config["job_title"].(string); ok && jt != "" {
				if res, err := exprEngine.EvaluateString(jt, exprCtx); err == nil && res != "" {
					jobTitle = res
				} else {
					jobTitle = jt
				}
			}
		}

		introduction := firstString(data, "introduction")
		if introduction == "" {
			if introTmpl, ok := config["introduction"].(string); ok && introTmpl != "" {
				if res, err := exprEngine.EvaluateString(introTmpl, exprCtx); err == nil && res != "" {
					introduction = res
				} else {
					introduction = introTmpl
				}
			}
		}

		category := firstString(data, "category")
		if category == "" {
			if catTmpl, ok := config["category"].(string); ok && catTmpl != "" {
				if res, err := exprEngine.EvaluateString(catTmpl, exprCtx); err == nil && res != "" {
					category = res
				} else {
					category = catTmpl
				}
			}
		}

		if aiRun {
			stored, err := peoplereview.StoredPerson(ctx, tx, profileID, platformUpper, username)
			if err != nil {
				return nil, fmt.Errorf("people.save: %w", err)
			}
			// The row keeps the stored category and introduction where the
			// item names none (the upsert's COALESCE).
			kept, keptIntro := category, introduction
			if kept == "" {
				kept = stored.Category
			}
			if keptIntro == "" {
				keptIntro = stored.Introduction
			}
			final, err := peoplereview.NotOperatorCategory(stored, kept, category != "", keptIntro, "a workflow run an AI started or changed")
			if err != nil {
				return nil, fmt.Errorf("people.save: %s/%s: %w", platformUpper, username, err)
			}
			if final != kept {
				category = final
			}
		}

		isVerified := nullableBool(data, "is_verified")

		followerCount := toNumericString(data, "follower_count", "followers_count", "followersCount")
		followingCount := toNumericString(data, "following_count")
		contentCount := toNumericString(data, "content_count")

		var followerInt, followingInt, contentInt int64
		if followerCount != "" {
			followerInt, _ = util.ConvertAbbreviatedNumber(stripWordSuffix(followerCount))
		}
		if followingCount != "" {
			followingInt, _ = util.ConvertAbbreviatedNumber(stripWordSuffix(followingCount))
		}
		if contentCount != "" {
			contentInt, _ = util.ConvertAbbreviatedNumber(stripWordSuffix(contentCount))
		}

		_, err := stmt.ExecContext(ctx,
			uuid.New().String(),
			username,
			platformUpper,
			nullableStr(fullName),
			nullableStr(imageURL),
			nil, // contact_details
			nullableStr(website),
			nullableInt(contentInt),
			nullableInt(followerInt),
			nullableInt(followingInt),
			nullableStr(introduction),
			isVerified,
			nullableStr(category),
			nullableStr(jobTitle),
			nullableStr(prof.Headline),
			nullableStr(prof.Location),
			nullableStr(prof.About),
			nullableStr(prof.Experience),
			nullableStr(prof.Education),
			nullableStr(prof.Details),
			nullableStr(profileURL),
			profileID,
			now,
			now,
		)
		if err != nil {
			return nil, fmt.Errorf("people.save: upsert %s/%s: %w", platformUpper, username, err)
		}

		// Emit the saved item enriched with resolved username.
		out := make(map[string]interface{}, len(data)+4)
		for k, v := range data {
			out[k] = v
		}
		out["platform_username"] = username
		out["platform"] = platformUpper
		if profileURL != "" {
			out["profile_url"] = profileURL
		}
		if category != "" {
			out["category"] = category
		}
		if introduction != "" {
			out["introduction"] = introduction
		}
		savedItems = append(savedItems, workflow.NewItem(out))
		if imageURL != "" {
			saved = append(saved, personphoto.Ref{Platform: platformUpper, Username: username})
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("people.save: commit: %w", err)
	}
	// Keep a copy of each photo; one that can't be fetched keeps its URL.
	_, _ = personphoto.Localize(ctx, globalPeopleDB, profileID, saved)

	return []workflow.NodeOutput{
		{Handle: "main", Items: savedItems},
	}, nil
}

// platformFromURL infers the platform from a URL's host.
func platformFromURL(u string) string {
	lowerURL := strings.ToLower(u)
	switch {
	case strings.Contains(lowerURL, "linkedin.com"):
		return "LINKEDIN"
	case strings.Contains(lowerURL, "instagram.com"):
		return "INSTAGRAM"
	case strings.Contains(lowerURL, "twitter.com") || strings.Contains(lowerURL, "x.com"):
		return "X"
	case strings.Contains(lowerURL, "tiktok.com"):
		return "TIKTOK"
	}
	return ""
}

// firstString returns the first non-empty string value found under the given keys.
func firstString(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// toNumericString returns a string value for the first matching key.
func toNumericString(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case float64:
			return fmt.Sprintf("%d", int64(v))
		case int64:
			return fmt.Sprintf("%d", v)
		case int:
			return fmt.Sprintf("%d", v)
		}
	}
	return ""
}

// stripWordSuffix removes trailing words like "followers", "posts" from count strings.
func stripWordSuffix(s string) string {
	parts := strings.Fields(s)
	if len(parts) > 0 {
		return parts[0]
	}
	return s
}

func nullableStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt(n int64) interface{} {
	if n == 0 {
		return nil
	}
	return n
}

// nullableBool binds the bool value only when the key is present; otherwise it
// binds NULL so the upsert's COALESCE preserves the existing stored flag.
func nullableBool(m map[string]interface{}, key string) interface{} {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return nil
}
