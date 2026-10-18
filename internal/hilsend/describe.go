package hilsend

import (
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// rule says when a node type is an outbound send and where its parts live in
// the node's config. ops nil means every operation (including none) sends.
type rule struct {
	action  string
	ops     []string
	text    []string
	subject []string
	to      []string
}

var (
	textKeys = []string{"messageText", "commentText", "message", "text", "body", "content", "comment", "caption", "status", "title"}
	toKeys   = []string{"to", "recipient", "channel", "chat_id", "channel_id", "user", "phone"}
)

// outbound lists the non-browser node types that send. Browser nodes are
// matched by their action name in socialRule.
var outbound = map[string]rule{
	"comm.email_send":   {action: ActionEmail, text: []string{"body"}, subject: []string{"subject"}, to: []string{"to"}},
	"comm.outlook_send": {action: ActionEmail, text: []string{"body"}, subject: []string{"subject"}, to: []string{"to"}},
	"service.outlook_mail": {action: ActionEmail, ops: []string{"send_message", "send_draft", "reply"},
		text: []string{"body"}, subject: []string{"subject"}, to: []string{"to"}},
	"service.gmail": {action: ActionEmail, ops: []string{"send_message", "send_draft", "reply"},
		text: []string{"body"}, subject: []string{"subject"}, to: []string{"to"}},
	"comm.slack":       {action: ActionPost, ops: []string{"post_message", "upload_file"}, text: []string{"text"}, to: []string{"channel"}},
	"comm.telegram":    {action: ActionDM, ops: []string{"", "send_message", "send_photo"}, text: []string{"message", "text"}, to: []string{"chat_id"}},
	"comm.discord":     {action: ActionPost, ops: []string{"", "send_message", "send_embed"}, text: []string{"content", "embed_description"}, to: []string{"channel_id", "channel"}},
	"comm.twilio":      {action: ActionDM, ops: []string{"send_sms", "send_whatsapp", "make_call"}, text: []string{"body", "message", "twiml"}, to: []string{"to"}},
	"comm.whatsapp":    {action: ActionDM, ops: []string{"send_message", "send_template", "send_media"}, text: []string{"message", "text", "body"}, to: []string{"to"}},
	"comm.reddit":      {action: ActionPost, ops: []string{"submit_post", "reply_to_comment"}, text: []string{"text", "body"}, subject: []string{"title"}, to: []string{"subreddit", "thing_id"}},
	"comm.mastodon":    {action: ActionPost, ops: []string{"create_status"}, text: []string{"status", "text"}},
	"comm.bluesky":     {action: ActionPost, ops: []string{"create_post"}, text: []string{"text"}},
	"service.reddit":   {action: ActionPost, ops: []string{"submit_post", "comment"}, text: []string{"text", "body"}, subject: []string{"title"}, to: []string{"subreddit", "thing_id"}},
	"service.mastodon": {action: ActionPost, ops: []string{"publish_status"}, text: []string{"status", "text"}},
	"service.bluesky":  {action: ActionPost, ops: []string{"create_post"}, text: []string{"text"}},
	"service.discord":  {action: ActionPost, ops: []string{"send_message", "send_embed"}, text: []string{"content", "embed_description"}, to: []string{"channel_id", "channel"}},
	"service.devto": {action: ActionPublish, ops: []string{"publish_article", "create_comment"}, text: []string{"body_markdown", "body"},
		subject: []string{"title"}},
	"service.hashnode":    {action: ActionPublish, ops: []string{"publish_post"}, text: []string{"content_markdown"}, subject: []string{"title"}},
	"service.producthunt": {action: ActionComment, ops: []string{"create_comment"}, text: []string{"body"}},
	"service.youtube":     {action: ActionPublish, ops: []string{"upload_video", "reply_to_comment"}, text: []string{"description", "text"}, subject: []string{"title"}},
}

// socialActions maps the action part of a browser node type
// (<platform>.<action>) to what it does.
var socialActions = map[string]string{
	"send_dms": ActionDM, "auto_reply_dms": ActionDM,
	"comment_on_posts": ActionComment, "comment_on_video": ActionComment, "comment_on_launch": ActionComment,
	"reply_to_comments": ActionComment, "reply_to_comment": ActionComment,
	"engage_with_posts": ActionComment, "engage_user_posts": ActionComment,
	"publish_post": ActionPublish, "submit_post": ActionPost,
	"share_video": ActionPublish, "duet_video": ActionPublish, "stitch_video": ActionPublish,
	"follow_users": ActionConnect, "follow_user": ActionConnect,
}

var socialPlatforms = map[string]bool{"instagram": true, "linkedin": true, "tiktok": true, "x": true, "hackernews": true, "producthunt": true}

// target is a classified send.
type target struct {
	action  string
	text    []string
	subject []string
	to      []string
	social  bool
	// channel names the app a Classifier's send goes through ("" for the
	// node types above); perItem lists the params its node resolves per
	// input item; toLabel and toUnset name its recipient (recipientOf).
	channel string
	perItem []string
	toLabel map[string]string
	toUnset string
	op      string
	show    []string
	exprs   []string
}

// classify reports whether nodeType with config is an outbound send.
func classify(nodeType string, config map[string]interface{}) (target, bool) {
	platform, act, _ := strings.Cut(nodeType, ".")
	if socialPlatforms[platform] {
		if a, ok := socialActions[act]; ok {
			return target{action: a, text: textKeys, social: true}, true
		}
		return target{}, false
	}
	if r, ok := outbound[nodeType]; ok && r.covers(config) {
		return target{action: r.action, text: r.text, subject: r.subject, to: r.to}, true
	}
	// An API package's annotated op: an app node's, or one a package adds
	// to a native node above (classifier.go).
	if c := classifierOf(); c != nil {
		if s, ok := c.Classify(nodeType, config); ok {
			return target{action: s.Action, text: s.Text, subject: s.Subject, to: s.To, channel: s.Channel, perItem: s.PerItem,
				toLabel: s.ToLabel, toUnset: s.ToUnset, op: s.Op, show: s.Show, exprs: s.Exprs}, true
		}
	}
	return target{}, false
}

// covers reports whether config's operation is one of r's sends.
func (r rule) covers(config map[string]interface{}) bool {
	if r.ops == nil {
		return true
	}
	op := str(config["operation"])
	for _, o := range r.ops {
		if o == op {
			return true
		}
	}
	return false
}

// IsOutbound reports whether running nodeType with config would send.
func IsOutbound(nodeType string, config map[string]interface{}) bool {
	_, ok := classify(nodeType, config)
	return ok
}

// MaySend reports whether running nodeType with config could send, judged
// before the run: it does (IsOutbound), or it is a node type that sends for
// some operations and its operation is an expression ("{{ … }}", or n8n's
// "=…") only the run resolves. A check made ahead of the run fails closed on
// it; the run itself resolves the operation before Guard sees it.
func MaySend(nodeType string, config map[string]interface{}) bool {
	if IsOutbound(nodeType, config) {
		return true
	}
	if r, ok := outbound[nodeType]; ok && r.ops != nil && isExpression(config["operation"]) {
		return true
	}
	c := classifierOf()
	return c != nil && (isExpression(config["operation"]) || isExpression(config["resource"])) && c.SendsSome(nodeType)
}

// isExpression reports whether v is a template the run resolves.
func isExpression(v interface{}) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	s = strings.TrimSpace(s)
	return strings.Contains(s, "{{") || strings.HasPrefix(s, "=")
}

func str(v interface{}) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case fmt.Stringer:
		return x.String()
	case float64, int, int64, bool:
		return fmt.Sprint(x)
	}
	return ""
}

// firstKey returns the first of keys that has a non-empty string value in m,
// or the first key when none has.
func firstKey(m map[string]interface{}, keys []string) (key, val string) {
	for _, k := range keys {
		if v := str(valueAt(m, k)); v != "" {
			return k, v
		}
	}
	if len(keys) > 0 {
		return keys[0], ""
	}
	return "", ""
}

// list reads a string list that may be a []string, a []interface{} or a
// comma/semicolon separated string.
func list(v interface{}) []string {
	var out []string
	switch x := v.(type) {
	case string:
		for _, p := range strings.FieldsFunc(x, func(r rune) bool { return r == ',' || r == ';' }) {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	case []string:
		for _, p := range x {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	case []interface{}:
		for _, e := range x {
			if p := str(e); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

var attachmentKeys = []string{"attachments", "media", "media_paths", "mediaPaths", "images", "image", "imagePath", "file", "file_path", "filePath", "photo", "video"}

// attachments describes the files and links a send carries. Sizes and types
// are read here from disk, never taken from what the agent claimed.
func attachments(config map[string]interface{}) []Attachment {
	out := []Attachment{}
	seen := map[string]bool{}
	for _, k := range attachmentKeys {
		for _, ref := range list(config[k]) {
			if seen[ref] {
				continue
			}
			seen[ref] = true
			if u, err := url.Parse(ref); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
				out = append(out, Attachment{Kind: "link", Name: filepath.Base(u.Path), URL: ref})
				continue
			}
			a := Attachment{Kind: "file", Name: filepath.Base(ref), Path: ref}
			if m := mime.TypeByExtension(strings.ToLower(filepath.Ext(ref))); m != "" {
				a.Mime = strings.SplitN(m, ";", 2)[0]
				if strings.HasPrefix(a.Mime, "image/") {
					a.Kind = "image"
				}
			}
			if st, err := os.Stat(ref); err == nil && st.Mode().IsRegular() {
				a.Size = st.Size()
			}
			out = append(out, a)
		}
	}
	return out
}

// one is a single send to prepare: a recipient and the config/input that
// performs exactly it. labelled: the recipient is a label, no person.
type one struct {
	recipient Recipient
	labelled  bool
	config    map[string]interface{}
	input     []map[string]interface{}
}

// split turns a node call into one send per recipient. Social nodes take a
// list of recipients (selectedListItems or the input items); each becomes its
// own item so the operator can judge them one by one. Other nodes address
// their recipients in config and make one item.
func split(t target, config map[string]interface{}, items []map[string]interface{}) []one {
	if !t.social {
		rec, labelled := t.recipientOf(config)
		return []one{{recipient: rec, labelled: labelled, config: config, input: items}}
	}
	var refs []interface{}
	if l, ok := config["selectedListItems"].([]interface{}); ok && len(l) > 0 {
		refs = l
	} else {
		for _, it := range items {
			refs = append(refs, it)
		}
	}
	if len(refs) == 0 {
		return []one{{config: config, input: items}}
	}
	var out []one
	for _, ref := range refs {
		c := cloneMap(config)
		c["selectedListItems"] = []interface{}{ref}
		o := one{config: c, input: []map[string]interface{}{{}}}
		switch r := ref.(type) {
		case string:
			o.recipient = Recipient{Name: lastSegment(r), ProfileURL: profileLike(r)}
			o.input = []map[string]interface{}{{"url": r}}
		case map[string]interface{}:
			u := firstStr(r, "profileUrl", "profile_url", "url", "href")
			name := firstStr(r, "name", "fullName", "full_name", "username")
			if name == "" {
				name = lastSegment(u)
			}
			o.recipient = Recipient{PersonID: firstStr(r, "personId", "person_id"), Name: name, ProfileURL: u}
			o.input = []map[string]interface{}{r}
		}
		out = append(out, o)
	}
	return out
}

func firstStr(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v := str(m[k]); v != "" {
			return v
		}
	}
	return ""
}

func profileLike(s string) string {
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return s
	}
	return ""
}

// lastSegment is the username in a profile URL, or s itself.
func lastSegment(s string) string {
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		return strings.TrimPrefix(parts[len(parts)-1], "@")
	}
	return strings.TrimPrefix(s, "@")
}

func cloneMap(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// secretKeys never reach the stored replay: the approved run resolves its
// credential again from the connection store.
var secretKeys = map[string]bool{
	"credential": true, "access_token": true, "refresh_token": true, "password": true, "token": true,
	"secret": true, "api_key": true, "apikey": true, "client_secret": true, "bot_token": true,
	"auth_token": true, "authorization": true, "private_key": true, "smtp_password": true,
}

// stripSecrets copies config without credentials: the keys a resolved
// credential merged in, and the well-known secret names.
func stripSecrets(config map[string]interface{}) map[string]interface{} {
	merged := map[string]bool{}
	if cred, ok := config["credential"].(map[string]interface{}); ok {
		for k := range cred {
			merged[k] = true
		}
	}
	out := make(map[string]interface{}, len(config))
	for k, v := range config {
		if secretKeys[strings.ToLower(k)] || merged[k] {
			continue
		}
		out[k] = v
	}
	return out
}

// inlineCredential reports whether config carries a credential written into
// it (an app node's "credential" map) rather than one resolved from a saved
// connection, which also fills its fields at the top level (credential_id,
// or `node run` finding the platform's connection). stripSecrets drops it,
// and the approved run would have none.
func inlineCredential(config map[string]interface{}) bool {
	cred, _ := config["credential"].(map[string]interface{})
	for k := range cred {
		if _, ok := config[k]; !ok {
			return true
		}
	}
	return false
}

// accountOf reads the sending account from config.
func accountOf(nodeType string, config map[string]interface{}) Account {
	platform, _, _ := strings.Cut(nodeType, ".")
	switch nodeType {
	case "comm.email_send":
		platform = "email"
	case "comm.outlook_send", "service.outlook_mail":
		platform = "outlook"
	case "service.gmail":
		platform = "gmail"
	}
	_, handle := firstKey(config, []string{"account", "from", "username", "identifier", "instance"})
	return Account{Platform: platform, Handle: handle}
}

// channelOf is the channel name shown on the card.
func channelOf(nodeType string) string {
	switch nodeType {
	case "comm.email_send":
		return "email"
	case "comm.outlook_send", "service.outlook_mail":
		return "outlook"
	}
	p, _, _ := strings.Cut(nodeType, ".")
	return p
}
