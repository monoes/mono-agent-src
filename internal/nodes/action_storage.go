package nodes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/monoes/mono-agent/internal/dbconn"
	"github.com/monoes/mono-agent/internal/personitem"
	"github.com/monoes/mono-agent/internal/personphoto"
	"github.com/monoes/mono-agent/internal/util"
	"github.com/monoes/mono-agent/internal/vault"
)

// workflowActionStorage implements action.StorageInterface backed by the
// workflow_node_targets / workflow_daily_counters tables (see
// data/migrations/030_workflow_action_bridge.sql), instead of the legacy
// actions/action_targets/action_daily_counters tables the standalone Actions
// page used. It is what BrowserNode.Execute passes to action.NewActionExecutor
// so that running a browser-automation node inside a workflow persists the
// same interaction history and honors the same daily rate caps the removed
// Actions page used to.
//
// UpdateActionState/UpdateActionReachedIndex are no-ops here: a workflow
// node's run state is already tracked by the workflow engine itself
// (workflow_executions/workflow_execution_nodes) — there is no separate
// per-action state machine to keep in sync.
type workflowActionStorage struct {
	db          *sql.DB
	profileID   string
	executionID string
	nodeID      string
	platform    string
	saved       bool // SaveExtractedData has stored items
}

// storageProfile is the execution's profile, or "" when ctx carries none (the
// storage then refuses to persist or count anything).
func storageProfile(ctx context.Context) string {
	pid, _ := vault.ProfileIDInContext(ctx)
	return pid
}

func (s *workflowActionStorage) UpdateActionState(id, state string) error { return nil }

func (s *workflowActionStorage) UpdateActionReachedIndex(id string, index int) error { return nil }

// SaveExtractedData writes one workflow_node_targets row per extracted item,
// upserting a people row for the person the item is about (a profile, or
// the author of a post or comment — see personitem.Resolve; extracted items
// reaching here have already been through NormalizeBrowserItem). Items that
// name no person (a post without an author, a company page) still get a
// workflow_node_targets row (person_id left null) so the interaction is
// still visible, just not linked to a person.
func (s *workflowActionStorage) SaveExtractedData(actionID string, items []map[string]interface{}) error {
	if s.db == nil || len(items) == 0 {
		return nil
	}
	s.saved = true
	if s.executionID == "" || s.nodeID == "" {
		// No workflow context to attach targets to (e.g. Execute called
		// outside a real workflow run) — nothing sensible to persist.
		return nil
	}

	// BEGIN IMMEDIATE: the execution is looked up before the first write, and a writer that commits in between
	// (nodes of one run save concurrently) would make the write fail at once with SQLITE_BUSY_SNAPSHOT, which
	// busy_timeout does not wait for (#1460).
	ctx := context.Background()
	var photos []personphoto.Ref
	var profileID string
	err := dbconn.WithWriteTx(ctx, s.db, func(tx *sql.Conn) error {
		profileID = s.profileID
		if profileID == "" {
			return errNoProfile
		}
		now := time.Now().UTC()

		// `node run` executes a node standalone under a synthetic execution ID
		// ("cli") that has no workflow_executions row; the FK would reject every
		// insert and fail the whole action. Keep the targets, unattached.
		executionID := sql.NullString{String: s.executionID, Valid: true}
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM workflow_executions WHERE id = ?`, s.executionID).Scan(&one); err != nil {
			if err != sql.ErrNoRows {
				return fmt.Errorf("nodes: looking up execution %s: %w", s.executionID, err)
			}
			executionID = sql.NullString{}
		}

		upsertPerson, err := tx.PrepareContext(ctx, `
		INSERT INTO people (id, platform_username, platform, full_name, image_url,
		        website, is_verified, job_title, headline, location, about,
		        experience, education, profile_details, profile_url, profile_id, created_at, updated_at,
		        follower_count, following_count, content_count)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(platform_username, platform, profile_id)
		DO UPDATE SET
		  full_name    = COALESCE(excluded.full_name, people.full_name),
		  image_url    = COALESCE(excluded.image_url, people.image_url),
		  website      = COALESCE(excluded.website, people.website),
		  is_verified  = COALESCE(excluded.is_verified, people.is_verified),
		  job_title    = COALESCE(excluded.job_title, people.job_title),
		  headline     = COALESCE(excluded.headline, people.headline),
		  location     = COALESCE(excluded.location, people.location),
		  about        = COALESCE(excluded.about, people.about),
		  experience   = COALESCE(excluded.experience, people.experience),
		  education    = COALESCE(excluded.education, people.education),
		  profile_details = `+personitem.DetailsMergeSQL+`,
		  profile_url  = COALESCE(people.profile_url, excluded.profile_url),
		  follower_count  = COALESCE(excluded.follower_count,  people.follower_count),
		  following_count = COALESCE(excluded.following_count, people.following_count),
		  content_count   = COALESCE(excluded.content_count,   people.content_count),
		  updated_at   = excluded.updated_at`)
		if err != nil {
			return fmt.Errorf("nodes: preparing person upsert: %w", err)
		}
		defer upsertPerson.Close()

		lookupPerson, err := tx.PrepareContext(ctx, `SELECT id FROM people WHERE platform_username = ? AND platform = ? AND profile_id = ?`)
		if err != nil {
			return fmt.Errorf("nodes: preparing person lookup: %w", err)
		}
		defer lookupPerson.Close()

		insertTarget, err := tx.PrepareContext(ctx, `
		INSERT INTO workflow_node_targets
		  (id, execution_id, node_id, person_id, platform, link, status, last_interacted_at, comment_text, metadata, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return fmt.Errorf("nodes: preparing target insert: %w", err)
		}
		defer insertTarget.Close()

		for _, item := range items {
			platform, _ := item["platform"].(string)
			if platform == "" {
				platform = s.platform
			}
			// The target's link is what the node acted on (a profile, a post).
			link, _ := item["url"].(string)
			if link == "" {
				link, _ = item["profile_url"].(string)
			}

			var personID sql.NullString
			if ref, ok := personitem.Resolve(platform, item); ok {
				// An item about a post or comment names its author: only the
				// author's name describes the person, the rest is the post's.
				// Extracted items never carry an introduction (the outreach
				// draft): a profile's bio is its About.
				var prof personitem.Profile
				var isVerified, followers, following, posts interface{}
				if ref.Author {
					prof.FullName = ref.FullName
				} else {
					prof = personitem.ProfileOf(item)
					if v, ok := item["is_verified"].(bool); ok && v {
						isVerified = 1
					}
					followers = itemCount(item, "followers_count", "follower_count")
					following = itemCount(item, "following_count")
					posts = itemCount(item, "content_count")
				}
				platformUpper := strings.ToUpper(platform)
				if _, err := upsertPerson.Exec(
					uuid.New().String(), ref.Username, platformUpper,
					nullIfEmpty(prof.FullName), nullIfEmpty(prof.ImageURL), nullIfEmpty(prof.Website),
					isVerified, nullIfEmpty(prof.JobTitle),
					nullIfEmpty(prof.Headline), nullIfEmpty(prof.Location), nullIfEmpty(prof.About),
					nullIfEmpty(prof.Experience), nullIfEmpty(prof.Education), nullIfEmpty(prof.Details),
					nullIfEmpty(ref.ProfileURL),
					profileID, now, now,
					followers, following, posts,
				); err != nil {
					return fmt.Errorf("nodes: upserting person %s: %w", ref.Username, err)
				}
				if prof.ImageURL != "" {
					photos = append(photos, personphoto.Ref{Platform: platformUpper, Username: ref.Username})
				}
				var pid string
				if err := lookupPerson.QueryRow(ref.Username, platformUpper, profileID).Scan(&pid); err == nil {
					personID = sql.NullString{String: pid, Valid: true}
				}
			}

			commentText, _ := item["comment_text"].(string)
			if commentText == "" {
				commentText, _ = item["response_text"].(string)
			}

			if _, err := insertTarget.Exec(
				uuid.New().String(), executionID, s.nodeID, personID, strings.ToUpper(platform),
				nullIfEmpty(link), "COMPLETED", now.Format(time.RFC3339), nullIfEmpty(commentText), nil, now,
			); err != nil {
				return fmt.Errorf("nodes: inserting workflow_node_target: %w", err)
			}
		}

		return nil
	})
	if err != nil {
		return err
	}
	// Keep a copy of each photo; one that can't be fetched keeps its URL.
	_, _ = personphoto.Localize(context.Background(), s.db, profileID, photos)
	return nil
}

// GetDailyActionCount and IncrementDailyActionCount key the daily-cap
// counter on "<platform>.<actionType>" (the same shape as the node's
// registered node_type, e.g. "instagram.follow_users") rather than the bare
// actionType action.ActionExecutor passes in — the legacy actions table
// scoped caps per-platform implicitly (each action row had its own
// target_platform); this makes that scoping explicit in the key itself.
func (s *workflowActionStorage) GetDailyActionCount(actionType string) (int, error) {
	if s.db == nil {
		return 0, nil
	}
	key, err := s.dailyCounterKey()
	if err != nil {
		return 0, err
	}
	var count int
	err = s.db.QueryRow(
		`SELECT count FROM workflow_daily_counters WHERE profile_key = ? AND node_type = ? AND day = ?`,
		key, s.nodeTypeKey(actionType), today(),
	).Scan(&count)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("nodes: reading daily action count: %w", err)
	}
	return count, nil
}

func (s *workflowActionStorage) IncrementDailyActionCount(actionType string) (int, error) {
	if s.db == nil {
		return 0, nil
	}
	key, err := s.dailyCounterKey()
	if err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(`
		INSERT INTO workflow_daily_counters (profile_key, node_type, day, count, updated_at)
		VALUES (?, ?, ?, 1, CURRENT_TIMESTAMP)
		ON CONFLICT(profile_key, node_type, day) DO UPDATE SET
		  count = count + 1, updated_at = CURRENT_TIMESTAMP`,
		key, s.nodeTypeKey(actionType), today(),
	); err != nil {
		return 0, fmt.Errorf("nodes: incrementing daily action count: %w", err)
	}
	return s.GetDailyActionCount(actionType)
}

// errNoProfile is returned when the storage has no profile: per-profile
// history and daily caps must never fall into a shared "default" bucket.
var errNoProfile = errors.New("nodes: action storage has no profile (the node ran without a profile)")

func (s *workflowActionStorage) dailyCounterKey() (string, error) {
	if s.profileID == "" {
		return "", errNoProfile
	}
	return s.profileID, nil
}

func (s *workflowActionStorage) nodeTypeKey(actionType string) string {
	return strings.ToLower(s.platform) + "." + strings.ToLower(actionType)
}

func today() string {
	return time.Now().UTC().Format("2006-01-02")
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// itemCount reads a count an extractor returned (a number, or text such as
// "176697"); nil when the item has none, so a stored count is left alone.
func itemCount(item map[string]interface{}, keys ...string) interface{} {
	for _, k := range keys {
		switch v := item[k].(type) {
		case int:
			return int64(v)
		case int64:
			return v
		case float64:
			return int64(v)
		case string:
			if n, err := util.ConvertAbbreviatedNumber(strings.ReplaceAll(strings.TrimSpace(v), ",", "")); err == nil {
				return n
			}
		}
	}
	return nil
}
