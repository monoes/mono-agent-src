package people

import (
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/sqltime"
)

// personInteraction is one thing a workflow did to a person
// (workflow_node_targets), as `people interactions` prints it.
type personInteraction struct {
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

// postSummary is one scraped post, with whether we liked or commented on it.
type postSummary struct {
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

// postComment is one scraped comment on a post.
type postComment struct {
	ID         string `json:"id"`
	Author     string `json:"author"`
	Text       string `json:"text"`
	Timestamp  string `json:"timestamp"`
	LikesCount int    `json:"likes_count"`
	ReplyCount int    `json:"reply_count"`
}

func (d Deps) newPeopleInteractionsCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:     "interactions <person-id>",
		Short:   "List what the profile's workflows did to a person, newest first",
		Example: `  monoagentcli --json people interactions 3f2a…`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit <= 0 {
				return d.InvalidInput("--limit must be positive")
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			rows, err := db.DB.Query(`
				SELECT wnt.execution_id, COALESCE(wn.name,''), COALESCE(wn.node_type,''),
				       wnt.platform, COALESCE(wnt.link,''), wnt.status,
				       COALESCE(wnt.comment_text,''),
				       COALESCE(wnt.last_interacted_at,''), COALESCE(wnt.created_at,'')
				FROM workflow_node_targets wnt
				JOIN workflow_executions we ON wnt.execution_id = we.id
				JOIN workflows w ON we.workflow_id = w.id
				LEFT JOIN workflow_nodes wn ON wnt.node_id = wn.id
				JOIN people p ON wnt.person_id = p.id
				WHERE wnt.person_id = ? AND w.profile_id = ?
				ORDER BY `+sqltime.SortKey("COALESCE(wnt.last_interacted_at, wnt.created_at)")+` DESC, wnt.rowid DESC
				LIMIT ?`, args[0], d.ProfileID(), limit)
			if err != nil {
				return fmt.Errorf("querying interactions: %w", err)
			}
			defer rows.Close()
			out := []personInteraction{}
			for rows.Next() {
				var i personInteraction
				if err := rows.Scan(&i.ExecutionID, &i.NodeName, &i.NodeType,
					&i.Platform, &i.Link, &i.Status, &i.CommentText,
					&i.LastInteractedAt, &i.CreatedAt); err != nil {
					return fmt.Errorf("scanning interaction: %w", err)
				}
				out = append(out, i)
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("iterating interactions: %w", err)
			}

			if d.JSONOutput() {
				return printReviewJSON(out)
			}
			if len(out) == 0 {
				fmt.Println("No interactions.")
				return nil
			}
			table := d.NewTable(os.Stdout, []string{"When", "Node", "Platform", "Status", "Link"}, nil)
			for _, i := range out {
				when := i.LastInteractedAt
				if when == "" {
					when = i.CreatedAt
				}
				table.Append([]string{truncateStr(when, 19), truncateStr(i.NodeName, 24), i.Platform, i.Status, truncateStr(i.Link, 50)})
			}
			table.Render()
			return nil
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 200, "Maximum number of results")
	return cmd
}

// newPeoplePostsCmd reads the posts and comments scraped from people's
// profiles.
func (d Deps) newPeoplePostsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "posts",
		Short: "Read posts and comments scraped from people's profiles",
	}
	cmd.AddCommand(d.newPeoplePostsListCmd(), d.newPeoplePostsGetCmd(), d.newPeoplePostsCommentsCmd())
	return cmd
}

// newPeoplePostsListCmd lists a person's posts. we_liked/we_commented come
// from the profile's completed like_posts / comment_on_posts workflow node
// targets on the post's URL (where MigrateActionsToWorkflows moved the
// legacy action_targets).
func (d Deps) newPeoplePostsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list <person-id>",
		Short:   "List a person's scraped posts, newest scrape first, with whether we liked or commented",
		Example: `  monoagentcli --json people posts list 3f2a…`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			rows, err := db.DB.Query(`
				SELECT p.id, p.shortcode, p.url,
				       COALESCE(p.thumbnail_url, ''), COALESCE(p.like_count, 0), COALESCE(p.comment_count, 0),
				       COALESCE(p.caption, ''), COALESCE(p.posted_at, ''), p.scraped_at,
				       EXISTS(SELECT 1 FROM workflow_node_targets wnt
				         JOIN workflow_nodes wn ON wnt.node_id = wn.id
				         JOIN workflows w ON wn.workflow_id = w.id
				         WHERE rtrim(wnt.link, '/') = rtrim(p.url, '/') AND wnt.status = 'COMPLETED'
				           AND wn.node_type LIKE '%.like_posts' AND w.profile_id = p.profile_id),
				       EXISTS(SELECT 1 FROM workflow_node_targets wnt
				         JOIN workflow_nodes wn ON wnt.node_id = wn.id
				         JOIN workflows w ON wn.workflow_id = w.id
				         WHERE rtrim(wnt.link, '/') = rtrim(p.url, '/') AND wnt.status = 'COMPLETED'
				           AND wn.node_type LIKE '%.comment_on_posts' AND w.profile_id = p.profile_id)
				FROM posts p
				JOIN people pe ON p.person_id = pe.id
				WHERE p.person_id = ? AND p.profile_id = ?
				ORDER BY p.scraped_at DESC`, args[0], d.ProfileID())
			if err != nil {
				return fmt.Errorf("querying posts: %w", err)
			}
			defer rows.Close()
			posts := []postSummary{}
			for rows.Next() {
				var p postSummary
				if err := rows.Scan(&p.ID, &p.Shortcode, &p.URL,
					&p.ThumbnailURL, &p.LikeCount, &p.CommentCount,
					&p.Caption, &p.PostedAt, &p.ScrapedAt,
					&p.WeLiked, &p.WeCommented); err != nil {
					return fmt.Errorf("scanning post: %w", err)
				}
				posts = append(posts, p)
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("iterating posts: %w", err)
			}

			if d.JSONOutput() {
				return printReviewJSON(posts)
			}
			if len(posts) == 0 {
				fmt.Println("No posts.")
				return nil
			}
			table := d.NewTable(os.Stdout, []string{"ID", "Likes", "Comments", "Liked", "Commented", "URL"}, nil)
			for _, p := range posts {
				table.Append([]string{p.ID, fmt.Sprint(p.LikeCount), fmt.Sprint(p.CommentCount),
					yesNo(p.WeLiked), yesNo(p.WeCommented), truncateStr(p.URL, 50)})
			}
			table.Render()
			return nil
		},
	}
}

func (d Deps) newPeoplePostsGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <post-id>",
		Short: "Show one scraped post",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			var p postSummary
			err = db.DB.QueryRow(`
				SELECT posts.id, shortcode, url,
				       COALESCE(thumbnail_url, ''), COALESCE(like_count, 0), COALESCE(comment_count, 0),
				       COALESCE(caption, ''), COALESCE(posted_at, ''), scraped_at
				FROM posts
				JOIN people ON posts.person_id = people.id
				WHERE posts.id = ? AND posts.profile_id = ?`, args[0], d.ProfileID(),
			).Scan(&p.ID, &p.Shortcode, &p.URL,
				&p.ThumbnailURL, &p.LikeCount, &p.CommentCount,
				&p.Caption, &p.PostedAt, &p.ScrapedAt)
			if errors.Is(err, sql.ErrNoRows) {
				return d.NotFound("post %q not found", args[0])
			}
			if err != nil {
				return fmt.Errorf("querying post: %w", err)
			}

			if d.JSONOutput() {
				// A single post doesn't carry the liked/commented flags.
				return printReviewJSON(struct {
					ID           string `json:"id"`
					Shortcode    string `json:"shortcode"`
					URL          string `json:"url"`
					ThumbnailURL string `json:"thumbnail_url"`
					LikeCount    int    `json:"like_count"`
					CommentCount int    `json:"comment_count"`
					Caption      string `json:"caption"`
					PostedAt     string `json:"posted_at"`
					ScrapedAt    string `json:"scraped_at"`
				}{p.ID, p.Shortcode, p.URL, p.ThumbnailURL, p.LikeCount, p.CommentCount, p.Caption, p.PostedAt, p.ScrapedAt})
			}
			fmt.Printf("%s\n%d likes · %d comments · posted %s · scraped %s\n", p.URL, p.LikeCount, p.CommentCount, p.PostedAt, p.ScrapedAt)
			if p.Caption != "" {
				fmt.Printf("\n%s\n", p.Caption)
			}
			return nil
		},
	}
}

func (d Deps) newPeoplePostsCommentsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "comments <post-id>",
		Short: "List a post's scraped comments, oldest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			rows, err := db.DB.Query(`
				SELECT post_comments.id, COALESCE(author, ''), COALESCE(text, ''),
				       COALESCE(timestamp, ''), COALESCE(likes_count, 0), COALESCE(reply_count, 0)
				FROM post_comments
				JOIN posts ON post_comments.post_id = posts.id
				JOIN people ON posts.person_id = people.id
				WHERE post_id = ? AND posts.profile_id = ?
				ORDER BY timestamp ASC`, args[0], d.ProfileID())
			if err != nil {
				return fmt.Errorf("querying comments: %w", err)
			}
			defer rows.Close()
			comments := []postComment{}
			for rows.Next() {
				var c postComment
				if err := rows.Scan(&c.ID, &c.Author, &c.Text, &c.Timestamp, &c.LikesCount, &c.ReplyCount); err != nil {
					return fmt.Errorf("scanning comment: %w", err)
				}
				comments = append(comments, c)
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("iterating comments: %w", err)
			}

			if d.JSONOutput() {
				return printReviewJSON(comments)
			}
			if len(comments) == 0 {
				fmt.Println("No comments.")
				return nil
			}
			table := d.NewTable(os.Stdout, []string{"Author", "Likes", "Replies", "Text"}, nil)
			for _, c := range comments {
				table.Append([]string{truncateStr(c.Author, 20), fmt.Sprint(c.LikesCount), fmt.Sprint(c.ReplyCount), truncateStr(c.Text, 60)})
			}
			table.Render()
			return nil
		},
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return ""
}
