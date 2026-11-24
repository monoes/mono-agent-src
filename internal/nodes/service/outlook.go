package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/apipkg"
	"github.com/monoes/mono-agent/internal/workflow"
)

// OutlookMailNode implements the service.outlook_mail node type, talking to
// Outlook/Hotmail over the Microsoft Graph REST API using an OAuth access
// token. That needs no XOAUTH2 and keeps working now that Microsoft has
// deprecated Basic Auth for IMAP on outlook.com/hotmail.com accounts.
//
// With "auth": "app_password" (or an app-password connection, detected from
// its keys) it reads over IMAP and sends over SMTP instead — see
// outlook_apppass.go. That mode is also the runtime of the legacy
// comm.outlook_read / comm.outlook_send types, registered as aliases.
type OutlookMailNode struct{}

func (n *OutlookMailNode) Type() string { return "service.outlook_mail" }

const outlookGraphBaseURL = "https://graph.microsoft.com/v1.0/me"

func (n *OutlookMailNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	if extensionOwns("service.outlook_mail", config) {
		return apipkg.ExecuteExtension(ctx, "service.outlook_mail", input, config)
	}
	if mode, err := outlookAuthMode(config); err != nil {
		return nil, err
	} else if mode == outlookAuthAppPassword {
		return outlookAppPasswordExecute(ctx, input, config)
	}
	accessToken := strVal(config, "access_token")
	if accessToken == "" {
		return nil, fmt.Errorf("outlook_mail: access_token is required")
	}
	if err := credentialScopeCheck(config, "outlook"); err != nil {
		return nil, fmt.Errorf("outlook_mail: %w", err)
	}
	operation := workflow.ConfigOperation(config)
	maxResults := intVal(config, "max_results")
	if maxResults == 0 {
		maxResults = 10
	}

	var items []workflow.Item

	switch operation {
	case "send_message":
		to := strVal(config, "to")
		subject := strVal(config, "subject")
		body := strVal(config, "body")
		bodyType := strVal(config, "body_type")
		if bodyType == "" {
			bodyType = "Text"
		} else if bodyType == "html" {
			bodyType = "HTML"
		} else {
			bodyType = "Text"
		}
		message := map[string]interface{}{
			"subject":      subject,
			"body":         map[string]interface{}{"contentType": bodyType, "content": body},
			"toRecipients": parseEmailAddresses(to),
		}
		if cc := parseEmailAddresses(strVal(config, "cc")); len(cc) > 0 {
			message["ccRecipients"] = cc
		}
		if bcc := parseEmailAddresses(strVal(config, "bcc")); len(bcc) > 0 {
			message["bccRecipients"] = bcc
		}
		sendBody := map[string]interface{}{"message": message}
		if _, err := outlookGraphRequest(ctx, "POST", outlookGraphBaseURL+"/sendMail", accessToken, sendBody); err != nil {
			return nil, fmt.Errorf("outlook_mail send_message: %w", err)
		}
		items = []workflow.Item{workflow.NewItem(map[string]interface{}{"status": "sent", "to": to, "subject": subject})}

	case "create_draft":
		to := strVal(config, "to")
		subject := strVal(config, "subject")
		body := strVal(config, "body")
		bodyType := strVal(config, "body_type")
		if bodyType == "html" {
			bodyType = "HTML"
		} else {
			bodyType = "Text"
		}
		draftBody := map[string]interface{}{
			"subject": subject,
			"body": map[string]interface{}{
				"contentType": bodyType,
				"content":     body,
			},
		}
		if recipients := parseEmailAddresses(to); len(recipients) > 0 {
			draftBody["toRecipients"] = recipients
		}
		if cc := parseEmailAddresses(strVal(config, "cc")); len(cc) > 0 {
			draftBody["ccRecipients"] = cc
		}
		if bcc := parseEmailAddresses(strVal(config, "bcc")); len(bcc) > 0 {
			draftBody["bccRecipients"] = bcc
		}
		// POST /me/messages (unlike /sendMail) saves the message to Drafts
		// without sending it.
		data, err := outlookGraphRequest(ctx, "POST", outlookGraphBaseURL+"/messages", accessToken, draftBody)
		if err != nil {
			return nil, fmt.Errorf("outlook_mail create_draft: %w", err)
		}
		items = []workflow.Item{workflow.NewItem(data)}

	case "create_reply", "reply":
		// Replies re-use the original message's recipients, subject, and
		// threading (conversationId/references) via Graph's dedicated reply
		// endpoints, instead of building a brand-new message. "create_reply"
		// saves a draft (mirrors create_draft); "reply" sends immediately
		// (mirrors send_message). reply_all=true replies to everyone on the
		// original message instead of just the sender.
		messageID := strVal(config, "message_id")
		if messageID == "" {
			return nil, fmt.Errorf("outlook_mail: message_id is required for %s", operation)
		}
		replyAll := boolVal(config, "reply_all")
		endpoint := "createReply"
		if operation == "reply" {
			endpoint = "reply"
		}
		if replyAll {
			endpoint += "All"
		}
		reqBody := map[string]interface{}{}
		if comment := strVal(config, "body"); comment != "" {
			reqBody["comment"] = comment
		}
		// Graph's reply endpoints accept an optional "message" object to
		// override/extend fields of the outgoing reply, e.g. adding
		// recipients beyond the original sender/participants.
		messageOverrides := map[string]interface{}{}
		if to := parseEmailAddresses(strVal(config, "to")); len(to) > 0 {
			messageOverrides["toRecipients"] = to
		}
		if cc := parseEmailAddresses(strVal(config, "cc")); len(cc) > 0 {
			messageOverrides["ccRecipients"] = cc
		}
		if bcc := parseEmailAddresses(strVal(config, "bcc")); len(bcc) > 0 {
			messageOverrides["bccRecipients"] = bcc
		}
		if len(messageOverrides) > 0 {
			reqBody["message"] = messageOverrides
		}
		data, err := outlookGraphRequest(ctx, "POST", outlookMessageURL(messageID, endpoint), accessToken, reqBody)
		if err != nil {
			return nil, fmt.Errorf("outlook_mail %s: %w", operation, err)
		}
		if operation == "reply" {
			data = map[string]interface{}{"status": "sent", "message_id": messageID, "reply_all": replyAll}
		}
		items = []workflow.Item{workflow.NewItem(data)}

	case "send_draft":
		messageID := strVal(config, "message_id")
		if messageID == "" {
			return nil, fmt.Errorf("outlook_mail: message_id is required for send_draft")
		}
		// Graph reassigns a new item id when a draft is sent (it moves from
		// Drafts into Sent Items), so messageID becomes stale the instant
		// /send succeeds — a later get_message/reply/delete_message against
		// it will fail. internetMessageId (the RFC 5322 Message-ID header)
		// is stable across that move, so grab it before sending and use it
		// to resolve the new id afterward.
		var internetMessageID string
		if data, err := outlookGraphRequest(ctx, "GET", outlookMessageURL(messageID, "")+"?$select=internetMessageId", accessToken, nil); err == nil {
			internetMessageID, _ = data["internetMessageId"].(string)
		}
		// POST /messages/{id}/send sends an existing draft as-is.
		if _, err := outlookGraphRequest(ctx, "POST", outlookMessageURL(messageID, "send"), accessToken, map[string]interface{}{}); err != nil {
			return nil, fmt.Errorf("outlook_mail send_draft: %w", err)
		}
		sentMessageID := messageID
		if internetMessageID != "" {
			// Graph's mailbox search index lags the send by a second or two,
			// so an immediate lookup usually finds nothing — retry briefly.
			for _, delay := range []time.Duration{0, 700 * time.Millisecond, 1500 * time.Millisecond} {
				if delay > 0 {
					time.Sleep(delay)
				}
				if newID, err := outlookFindSentIDByInternetMessageID(ctx, accessToken, internetMessageID); err == nil && newID != "" {
					sentMessageID = newID
					break
				}
			}
		}
		items = []workflow.Item{workflow.NewItem(map[string]interface{}{"status": "sent", "message_id": sentMessageID})}

	case "delete_message":
		messageID := strVal(config, "message_id")
		if messageID == "" {
			return nil, fmt.Errorf("outlook_mail: message_id is required for delete_message")
		}
		if _, err := outlookGraphRequest(ctx, "DELETE", outlookMessageURL(messageID, ""), accessToken, nil); err != nil {
			return nil, fmt.Errorf("outlook_mail delete_message: %w", err)
		}
		deleted := map[string]interface{}{"status": "deleted", "message_id": messageID}
		// The files kept for the mail go with it.
		if err := removeOutlookAttachments(ctx, messageID); err != nil {
			deleted["attachment_cleanup_error"] = err.Error()
		}
		items = []workflow.Item{workflow.NewItem(deleted)}

	case "list_messages":
		mailbox := strVal(config, "mailbox")
		if mailbox == "" {
			mailbox = "inbox"
		}
		data, err := outlookGraphRequest(ctx, "GET", outlookListMessagesURL(mailbox, maxResults, config), accessToken, nil)
		if err != nil {
			return nil, fmt.Errorf("outlook_mail list_messages: %w", err)
		}
		messages, _ := data["value"].([]interface{})
		items = make([]workflow.Item, 0, len(messages))
		for _, m := range messages {
			msg, ok := m.(map[string]interface{})
			if !ok {
				continue
			}
			enrichOutlookMessage(ctx, msg, accessToken, mailbox, downloadAttachmentsEnabled(config))
			items = append(items, workflow.NewItem(msg))
		}

	case "whoami":
		// Resolves the authenticated account's own address using the
		// smallest possible read: one field ("from" or "toRecipients") of
		// at most one message, no subject/body/content fetched. Tries
		// Sent Items first (its "from" is always the account owner); falls
		// back to Inbox ("toRecipients" — mail addressed to the owner) for
		// mailboxes that have never sent anything.
		address, source, err := outlookWhoAmI(ctx, accessToken)
		if err != nil {
			return nil, fmt.Errorf("outlook_mail whoami: %w", err)
		}
		items = []workflow.Item{workflow.NewItem(map[string]interface{}{"address": address, "source": source})}

	case "get_message":
		messageID := strVal(config, "message_id")
		if messageID == "" {
			return nil, fmt.Errorf("outlook_mail: message_id is required for get_message")
		}
		data, err := outlookGraphRequest(ctx, "GET", outlookMessageURL(messageID, ""), accessToken, nil)
		if err != nil {
			return nil, fmt.Errorf("outlook_mail get_message: %w", err)
		}
		enrichOutlookMessage(ctx, data, accessToken, strVal(config, "mailbox"), downloadAttachmentsEnabled(config))
		items = []workflow.Item{workflow.NewItem(data)}

	default:
		return nil, fmt.Errorf("outlook_mail: unknown operation %q", operation)
	}

	return []workflow.NodeOutput{{Handle: "main", Items: items}}, nil
}

// outlookMessageURL builds a /me/messages/{id}[/{action}] request URL with
// the message id path-escaped. Graph message ids can contain '?', '#', and
// '/', which would otherwise truncate or corrupt the URL path.
func outlookMessageURL(messageID, action string) string {
	u := outlookGraphBaseURL + "/messages/" + url.PathEscape(messageID)
	if action != "" {
		u += "/" + action
	}
	return u
}

// escapeODataString escapes a string for use inside a single-quoted OData
// ($filter) literal: single quotes are doubled, per the OData v4 ABNF —
// without this an address like o'brien@x.com breaks out of the literal.
func escapeODataString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// outlookListMessagesURL builds the list_messages request URL. $search
// (full-text over subject/body/sender, or a field-scoped query like
// `from:someone@x.com` or `subject:invoice`) and $filter are mutually
// exclusive in the same Graph request, so $search wins when given — it
// covers the more common "find this email" case directly. The mailbox
// segment and the from-address OData literal are config-influenced, so both
// are escaped (PathEscape and escapeODataString respectively).
func outlookListMessagesURL(mailbox string, maxResults int, config map[string]interface{}) string {
	u := fmt.Sprintf("%s/mailFolders/%s/messages?$top=%d&$select=id,subject,from,toRecipients,receivedDateTime,body,bodyPreview,isRead,hasAttachments,webLink",
		outlookGraphBaseURL, url.PathEscape(mailbox), maxResults)
	if search := strVal(config, "search"); search != "" {
		return u + "&$search=" + gmailURLEncode(`"`+search+`"`)
	}
	var filters []string
	if unreadOnly, _ := config["unread_only"].(bool); unreadOnly {
		filters = append(filters, "isRead eq false")
	}
	if from := strVal(config, "from_address"); from != "" {
		filters = append(filters, fmt.Sprintf("from/emailAddress/address eq '%s'", escapeODataString(from)))
	}
	if len(filters) > 0 {
		u += "&$filter=" + gmailURLEncode(strings.Join(filters, " and "))
	}
	return u
}

// parseEmailAddresses splits a comma/semicolon-separated address string (as
// produced by mail clients and CSV-style config input) into Graph API
// emailAddress recipient objects. A single address works too since it has no
// separators to split on.
func parseEmailAddresses(raw string) []map[string]interface{} {
	if raw == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' })
	out := make([]map[string]interface{}, 0, len(parts))
	for _, p := range parts {
		addr := strings.TrimSpace(p)
		if addr == "" {
			continue
		}
		out = append(out, map[string]interface{}{"emailAddress": map[string]interface{}{"address": addr}})
	}
	return out
}

// outlookFindSentIDByInternetMessageID looks up a Sent Items message's
// current Graph id by its stable internetMessageId, used to recover the new
// id Graph assigns when send_draft moves a message out of Drafts.
func outlookFindSentIDByInternetMessageID(ctx context.Context, accessToken, internetMessageID string) (string, error) {
	url := outlookGraphBaseURL + "/mailFolders/sentitems/messages?$filter=" +
		gmailURLEncode("internetMessageId eq '"+escapeODataString(internetMessageID)+"'") + "&$select=id&$top=1"
	data, err := outlookGraphRequest(ctx, "GET", url, accessToken, nil)
	if err != nil {
		return "", err
	}
	values, _ := data["value"].([]interface{})
	if len(values) == 0 {
		return "", nil
	}
	msg, _ := values[0].(map[string]interface{})
	id, _ := msg["id"].(string)
	return id, nil
}

// outlookWhoAmI resolves the mailbox owner's own address without the
// User.Read scope (not part of this app's OAuth scopes, and adding it would
// force every existing connection to be reconnected). Mail.Read/ReadWrite
// alone can't reach /me, so this reads the single smallest field that
// reveals the owner's identity from mail data instead.
func outlookWhoAmI(ctx context.Context, accessToken string) (address, source string, err error) {
	// A Sent Items message's "from" is always the account owner.
	data, err := outlookGraphRequest(ctx, "GET",
		outlookGraphBaseURL+"/mailFolders/sentitems/messages?$top=1&$select=from", accessToken, nil)
	if err == nil {
		if addr := firstEmailAddress(data, "from"); addr != "" {
			return addr, "sentitems", nil
		}
	}
	// Fall back to Inbox: mail addressed to the owner names them in toRecipients.
	data, err = outlookGraphRequest(ctx, "GET",
		outlookGraphBaseURL+"/mailFolders/inbox/messages?$top=1&$select=toRecipients", accessToken, nil)
	if err != nil {
		return "", "", err
	}
	if addr := firstEmailAddress(data, "toRecipients"); addr != "" {
		return addr, "inbox", nil
	}
	return "", "", fmt.Errorf("mailbox has no sent or received messages to resolve identity from")
}

// firstEmailAddress extracts the address from the first message's given
// field, which is either a single {"emailAddress":{...}} object ("from") or
// an array of them ("toRecipients").
func firstEmailAddress(data map[string]interface{}, field string) string {
	values, _ := data["value"].([]interface{})
	if len(values) == 0 {
		return ""
	}
	msg, _ := values[0].(map[string]interface{})
	switch field {
	case "from":
		fromObj, _ := msg["from"].(map[string]interface{})
		ea, _ := fromObj["emailAddress"].(map[string]interface{})
		addr, _ := ea["address"].(string)
		return addr
	case "toRecipients":
		recipients, _ := msg["toRecipients"].([]interface{})
		if len(recipients) == 0 {
			return ""
		}
		rm, _ := recipients[0].(map[string]interface{})
		ea, _ := rm["emailAddress"].(map[string]interface{})
		addr, _ := ea["address"].(string)
		return addr
	}
	return ""
}

// outlookGraphRequest makes an authenticated request to the Microsoft Graph API.
func outlookGraphRequest(ctx context.Context, method, url, accessToken string, body interface{}) (map[string]interface{}, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("outlook_mail: marshaling body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("outlook_mail: creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("outlook_mail %s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("outlook_mail: reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("outlook_mail HTTP %d: %s", resp.StatusCode, string(respBytes))
	}
	if len(respBytes) == 0 {
		return map[string]interface{}{}, nil
	}
	var result map[string]interface{}
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("outlook_mail: parsing JSON: %w", err)
	}
	return result, nil
}
