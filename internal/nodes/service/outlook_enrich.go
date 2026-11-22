package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/attachments"
)

// downloadAttachmentsEnabled reports whether fetched messages should have
// their attachments downloaded. On by default: a synced message that says
// "invoice.pdf attached" is useless to a reader who cannot open the file.
// Set "download_attachments": false to skip the extra requests and disk use.
func downloadAttachmentsEnabled(config map[string]interface{}) bool {
	if v, ok := config["download_attachments"].(bool); ok {
		return v
	}
	return true
}

// enrichOutlookMessage adds, in place, the two things a downstream reader needs
// beyond the mail text itself:
//
//   - provenance ("_source"): which system this came from, which mailbox and
//     folder it was read from, its id there, and a link back to the original —
//     so anyone reading the message later can say where it came from and go
//     look at it.
//   - attachments: every file on the message, downloaded to disk, with its
//     local path recorded so a reader can actually open it.
//
// Attachment failures are recorded on the attachment entry rather than failing
// the whole sync — one unreadable file must not cost you the mail.
func enrichOutlookMessage(ctx context.Context, msg map[string]interface{}, accessToken, mailbox string, download bool) {
	if mailbox == "" {
		mailbox = "inbox"
	}
	messageID, _ := msg["id"].(string)
	webLink, _ := msg["webLink"].(string)

	msg["_source"] = map[string]interface{}{
		"source":      "outlook",
		"via":         "service.outlook_mail (Microsoft Graph)",
		"folder":      mailbox,
		"external_id": messageID,
		"web_link":    webLink,
		"fetched_at":  time.Now().UTC().Format(time.RFC3339),
	}

	hasAttachments, _ := msg["hasAttachments"].(bool)
	if !download || !hasAttachments || messageID == "" {
		return
	}
	files, err := downloadOutlookAttachments(ctx, accessToken, messageID)
	if err != nil {
		msg["attachment_error"] = err.Error()
		return
	}
	msg["attachments"] = files
	msg["attachment_count"] = len(files)
}

// downloadOutlookAttachments fetches every attachment on a message and writes
// the file ones to disk. Graph returns three attachment shapes; only
// fileAttachment carries bytes, so item/reference attachments are reported
// with a note instead of a path.
func downloadOutlookAttachments(ctx context.Context, accessToken, messageID string) ([]map[string]interface{}, error) {
	data, err := outlookGraphRequest(ctx, "GET",
		outlookMessageURL(messageID, "attachments"), accessToken, nil)
	if err != nil {
		return nil, fmt.Errorf("list attachments: %w", err)
	}
	raw, _ := data["value"].([]interface{})

	out := make([]map[string]interface{}, 0, len(raw))
	seen := map[string]bool{} // paths written for this message in this pass
	for _, a := range raw {
		att, ok := a.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := att["name"].(string)
		entry := map[string]interface{}{
			"filename":     name,
			"content_type": att["contentType"],
			"size_bytes":   att["size"],
		}

		b64, _ := att["contentBytes"].(string)
		if b64 == "" {
			odataType, _ := att["@odata.type"].(string)
			entry["note"] = fmt.Sprintf("not a file attachment (%s) — nothing to download", odataType)
			out = append(out, entry)
			continue
		}
		decoded, derr := base64.StdEncoding.DecodeString(b64)
		if derr != nil {
			entry["error"] = fmt.Sprintf("decode: %v", derr)
			out = append(out, entry)
			continue
		}
		path, werr := attachments.SaveDistinct(messageID, name, decoded, seen)
		if werr != nil {
			entry["error"] = werr.Error()
			out = append(out, entry)
			continue
		}
		entry["path"] = path
		entry["size_bytes"] = len(decoded)
		out = append(out, entry)
	}
	return out, nil
}
