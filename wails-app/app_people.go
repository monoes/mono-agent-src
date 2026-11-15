package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// People
// ─────────────────────────────────────────────────────────────────────────────

type PersonInfo struct {
	ID             string `json:"id"`
	Username       string `json:"username"`
	Platform       string `json:"platform"`
	FullName       string `json:"full_name"`
	ImageURL       string `json:"image_url"`
	ProfileURL     string `json:"profile_url"`
	FollowerCount  string `json:"follower_count"`
	FollowingCount int    `json:"following_count"`
	IsVerified     bool   `json:"is_verified"`
	JobTitle       string `json:"job_title"`
	Category       string `json:"category"`
	CreatedAt      string `json:"created_at"`
	// Kind is "person" or "company"; OwnAccount marks our own account
	// (the person a post with no recipient is filed under).
	Kind       string `json:"kind"`
	OwnAccount bool   `json:"own_account"`
}

type PersonDetailInfo struct {
	ID             string `json:"id"`
	Username       string `json:"username"`
	Platform       string `json:"platform"`
	FullName       string `json:"full_name"`
	ImageURL       string `json:"image_url"`
	ProfileURL     string `json:"profile_url"`
	FollowerCount  string `json:"follower_count"`
	FollowingCount int    `json:"following_count"`
	ContentCount   int    `json:"content_count"`
	IsVerified     bool   `json:"is_verified"`
	JobTitle       string `json:"job_title"`
	Category       string `json:"category"`
	Introduction   string `json:"introduction"`
	Website        string `json:"website"`
	ContactDetails string `json:"contact_details"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	// Profile details from a profile read (LinkedIn): experience entries
	// carry title/company/date_range/location/description…, education
	// entries school/degree/field_of_study/date_range… (see
	// data/migrations/053_people_profile_details.sql).
	Headline   string                   `json:"headline"`
	Location   string                   `json:"location"`
	About      string                   `json:"about"`
	Experience []map[string]interface{} `json:"experience"`
	Education  []map[string]interface{} `json:"education"`
	// ProfileDetails holds a profile read's platform extras (links,
	// pronouns, likes, join date, verification type, pinned post…; see
	// data/migrations/054_people_profile_extras.sql). Nil when none.
	ProfileDetails map[string]interface{} `json:"profile_details"`
}

type PersonInteraction struct {
	ExecutionID      string `json:"execution_id"`
	NodeName         string `json:"node_name"`
	NodeType         string `json:"node_type"`
	Platform         string `json:"platform"`
	Link             string `json:"link"`
	Status           string `json:"status"`
	CommentText      string `json:"comment_text"`
	LastInteractedAt string `json:"last_interacted_at"`
	CreatedAt        string `json:"created_at"`
}

// PostSummary is returned by GetPersonPosts.
type PostSummary struct {
	ID           string `json:"id"`
	Shortcode    string `json:"shortcode"`
	URL          string `json:"url"`
	ThumbnailURL string `json:"thumbnail_url"`
	LikeCount    int    `json:"like_count"`
	CommentCount int    `json:"comment_count"`
	Caption      string `json:"caption"`
	PostedAt     string `json:"posted_at"`
	ScrapedAt    string `json:"scraped_at"`
	WeLiked      bool   `json:"we_liked"`
	WeCommented  bool   `json:"we_commented"`
}

// PostDetail is returned by GetPostDetail.
type PostDetail struct {
	ID           string `json:"id"`
	Shortcode    string `json:"shortcode"`
	URL          string `json:"url"`
	ThumbnailURL string `json:"thumbnail_url"`
	LikeCount    int    `json:"like_count"`
	CommentCount int    `json:"comment_count"`
	Caption      string `json:"caption"`
	PostedAt     string `json:"posted_at"`
	ScrapedAt    string `json:"scraped_at"`
}

// PostComment is returned by GetPostComments.
type PostComment struct {
	ID         string `json:"id"`
	Author     string `json:"author"`
	Text       string `json:"text"`
	Timestamp  string `json:"timestamp"`
	LikesCount int    `json:"likes_count"`
	ReplyCount int    `json:"reply_count"`
}

// peopleFilterArgs turns the People page's filter into `people list|count`
// flags; "ALL" means no platform filter.
func peopleFilterArgs(platform, search string) []string {
	var args []string
	if platform != "" && platform != "ALL" {
		args = append(args, "--platform="+platform)
	}
	if search != "" {
		args = append(args, "--search="+search)
	}
	return args
}

// peopleReadTimeout bounds a People page read, so a hung CLI ends in an error
// instead of a spinner that never stops. A variable so a test can shorten it.
var peopleReadTimeout = 60 * time.Second

// runReadCLI is runMonoCLI for a read the page waits on: it gives up after
// peopleReadTimeout with a plain message, and returns the CLI's own error text.
func (a *App) runReadCLI(result interface{}, args ...string) error {
	fullArgs, err := a.scopedArgs(args...)
	if err != nil {
		return err
	}
	parent := a.ctx
	if parent == nil { // before startup, and in tests
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, peopleReadTimeout)
	defer cancel()
	err = a.runCLIArgsCtx(ctx, "", result, fullArgs)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("the command did not answer in %d s", int(peopleReadTimeout/time.Second))
	}
	return err
}

// GetPeople returns one page of the active profile's people, newest first —
// `people list`. limit <= 0 means every person (and ignores offset).
func (a *App) GetPeople(platform, search string, limit, offset int) ([]PersonInfo, error) {
	if a.signedOut() != nil {
		return nil, nil
	}
	args := append([]string{"people", "list"}, peopleFilterArgs(platform, search)...)
	if limit > 0 {
		args = append(args, "--limit", strconv.Itoa(limit), "--offset", strconv.Itoa(offset))
	} else {
		args = append(args, "--limit", "0")
	}
	// The CLI calls the username platform_username.
	var rows []struct {
		PersonInfo
		PlatformUsername string `json:"platform_username"`
	}
	if err := a.runReadCLI(&rows, args...); err != nil {
		return nil, err
	}
	people := make([]PersonInfo, len(rows))
	for i, r := range rows {
		people[i] = r.PersonInfo
		people[i].Username = r.PlatformUsername
	}
	return people, nil
}

// GetPeopleCount is the People page's total for the same filter —
// `people count`.
func (a *App) GetPeopleCount(platform, search string) (int, error) {
	if a.signedOut() != nil {
		return 0, nil
	}
	var res struct {
		Count int `json:"count"`
	}
	if err := a.runReadCLI(&res, append([]string{"people", "count"}, peopleFilterArgs(platform, search)...)...); err != nil {
		return 0, err
	}
	return res.Count, nil
}

// DeletePeople deletes people (and their saved photos) from the active
// profile — `people delete`.
func (a *App) DeletePeople(ids []string) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	return a.runMonoCLI("", nil, append([]string{"people", "delete"}, ids...)...)
}

// GetPersonDetail returns one person, or nil when not in the active
// profile — `people get`.
func (a *App) GetPersonDetail(id string) (*PersonDetailInfo, error) {
	if a.signedOut() != nil {
		return nil, nil
	}
	var p struct {
		PersonDetailInfo
		PlatformUsername string `json:"platform_username"`
	}
	if err := a.runReadCLI(&p, "people", "get", id); err != nil {
		return nil, err
	}
	p.Username = p.PlatformUsername
	return &p.PersonDetailInfo, nil
}

// GetPersonInteractions lists what the profile's workflows did to a person,
// newest first — `people interactions`.
func (a *App) GetPersonInteractions(id string) ([]PersonInteraction, error) {
	if a.signedOut() != nil {
		return nil, nil
	}
	var out []PersonInteraction
	if err := a.runReadCLI(&out, "people", "interactions", id); err != nil {
		return nil, err
	}
	return out, nil
}

// PendingPersonApproval represents a lead or contact waiting for human review in Human-in-Loop.
type PendingPersonApproval struct {
	ID               string `json:"id"`
	Platform         string `json:"platform"`
	PlatformUsername string `json:"platform_username"`
	FullName         string `json:"full_name"`
	ImageUrl         string `json:"image_url"`
	ProfileUrl       string `json:"profile_url"`
	JobTitle         string `json:"job_title"`
	Category         string `json:"category"`
	Introduction     string `json:"introduction"`
	CreatedAt        string `json:"created_at"`
	// Suggestion is TypeSafe Jev's cached suggestion ({suggest, p,
	// intro_fit, intro_fit_p, model, at}) when the profile enabled surface
	// people_review; absent otherwise.
	Suggestion map[string]interface{} `json:"suggestion,omitempty"`
}

// GetPendingPeopleApprovals returns the active profile's people staged for
// review (category "pending_approval") — `people review list --suggest`.
func (a *App) GetPendingPeopleApprovals() ([]*PendingPersonApproval, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	results := []*PendingPersonApproval{}
	if err := a.runMonoCLI("", &results, "people", "review", "list", "--suggest"); err != nil {
		return nil, err
	}
	return results, nil
}

// ApprovePendingPerson approves a person, replacing the introduction when
// editedIntroduction is set — `people review approve`. With sendNow the CLI
// also picks the profile's dispatch workflow (refusing, before approving,
// when there is none, several or only an inactive one) and returns the run,
// which starts here like any GUI run so it shows up in the run tracking.
func (a *App) ApprovePendingPerson(personID, editedIntroduction string, sendNow bool) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	args := []string{"people", "review", "approve", personID}
	if strings.TrimSpace(editedIntroduction) != "" {
		args = append(args, "--intro", editedIntroduction)
	}
	if sendNow {
		args = append(args, "--send-plan")
	}
	var res struct {
		Send *struct {
			WorkflowID string                 `json:"workflow_id"`
			Input      map[string]interface{} `json:"input"`
		} `json:"send"`
	}
	if err := a.runMonoCLI("", &res, args...); err != nil {
		return err
	}
	if res.Send == nil {
		return nil
	}
	input, err := json.Marshal(res.Send.Input)
	if err != nil {
		return err
	}
	if err := a.RunWorkflowWithInput(res.Send.WorkflowID, string(input)); err != nil {
		return fmt.Errorf("approved, but sending failed: %w", err)
	}
	return nil
}

// RejectPendingPerson takes a person out of the queue — `people review reject`.
func (a *App) RejectPendingPerson(personID string) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	return a.runMonoCLI("", nil, "people", "review", "reject", personID)
}

// GetLatestPersonStatus returns the most recent status update for a person,
// or nil if none exists yet — `people status get`.
func (a *App) GetLatestPersonStatus(personId string) *PersonStatusUpdate {
	if a.signedOut() != nil {
		return nil
	}
	var u *PersonStatusUpdate
	if err := a.runMonoCLI("", &u, "people", "status", "get", personId); err != nil {
		return nil
	}
	return u
}

// AddPersonStatus appends a new status update for a person —
// `people status set`.
func (a *App) AddPersonStatus(personId, text string) (*PersonStatusUpdate, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	var u PersonStatusUpdate
	if err := a.runMonoCLI("", &u, "people", "status", "set", "--", personId, text); err != nil {
		return nil, err
	}
	return &u, nil
}

// GetPersonStatusHistory returns every status update for a person, newest
// first — `people status history`. limit <= 0 means no cap.
func (a *App) GetPersonStatusHistory(personId string, limit int) []*PersonStatusUpdate {
	if a.signedOut() != nil {
		return nil
	}
	args := []string{"people", "status", "history", personId}
	if limit > 0 {
		args = append(args, "--limit", strconv.Itoa(limit))
	}
	var updates []*PersonStatusUpdate
	if err := a.runMonoCLI("", &updates, args...); err != nil {
		return nil
	}
	return updates
}

// GetPersonPosts returns all scraped posts for a person, with
// we_liked/we_commented flags — `people posts list`.
func (a *App) GetPersonPosts(personID string) []PostSummary {
	if a.signedOut() != nil {
		return nil
	}
	posts := []PostSummary{}
	if err := a.runMonoCLI("", &posts, "people", "posts", "list", personID); err != nil || posts == nil {
		return []PostSummary{}
	}
	return posts
}

// GetPostDetail returns full metadata for a single post by ID —
// `people posts get`.
func (a *App) GetPostDetail(postID string) *PostDetail {
	if a.signedOut() != nil {
		return nil
	}
	var p PostDetail
	if err := a.runMonoCLI("", &p, "people", "posts", "get", postID); err != nil {
		return nil
	}
	return &p
}

// GetPostComments returns all scraped comments for a post, oldest first —
// `people posts comments`.
func (a *App) GetPostComments(postID string) []PostComment {
	if a.signedOut() != nil {
		return nil
	}
	comments := []PostComment{}
	if err := a.runMonoCLI("", &comments, "people", "posts", "comments", postID); err != nil || comments == nil {
		return []PostComment{}
	}
	return comments
}
