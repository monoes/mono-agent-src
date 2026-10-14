package service

// RedditNodeSchema documents the config keys RedditNode.Execute reads out of
// its map[string]interface{} config — see internal/nodes/control/set_schema.go's
// doc comment for why this is a companion struct rather than the runtime
// config, and internal/tools/schemagen for the tag grammar.
//
// credential_platform: reddit
type RedditNodeSchema struct {
	CredentialID string `json:"credential_id" schema:"label=Reddit Connection,type=credential_picker,required"`

	Operation string `json:"operation" schema:"label=Operation,type=select,required,options=submit_post|get_hot|comment|upvote|list_comments|get_post_metrics,default=submit_post"`

	Subreddit string `json:"subreddit" schema:"label=Subreddit,type=text,depends_on_key=operation,depends_on_values=submit_post|get_hot"`

	Title string `json:"title" schema:"label=Post Title,type=text,depends_on_key=operation,depends_on_values=submit_post"`

	Kind string `json:"kind" schema:"label=Post Kind,type=select,options=auto|self|link,default=auto,help=auto posts a link when a URL is set and a text post otherwise; self is a text post; link is a URL post (requires url).,depends_on_key=operation,depends_on_values=submit_post"`

	Text string `json:"text" schema:"label=Text,type=textarea,rows=4,help=Post body (submit_post with kind=self) or comment text.,depends_on_key=operation,depends_on_values=submit_post|comment"`

	URL string `json:"url" schema:"label=Link URL,type=text,help=Required for submit_post with kind=link.,depends_on_key=operation,depends_on_values=submit_post"`

	ThingID string `json:"thing_id" schema:"label=Thing ID (fullname),type=text,placeholder=e.g. t3_abc123 (post)， t1_xyz (comment),help=Parent fullname for comment， target fullname for upvote， or the post (t3_... or bare ID) for list_comments/get_post_metrics.,depends_on_key=operation,depends_on_values=comment|upvote|list_comments|get_post_metrics"`

	Limit float64 `json:"limit" schema:"label=Limit,type=number,default=25,depends_on_key=operation,depends_on_values=get_hot"`

	UserAgent string `json:"user_agent" schema:"label=User-Agent,type=text,default=mono-agent/1.0,help=Reddit blocks generic user agents; set something descriptive."`
}
