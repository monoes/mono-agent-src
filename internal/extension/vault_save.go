package extension

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/profiledir"
)

// vault.save: "keep this file in my vault" (#451), the side panel's confirm
// card. The extension sends one image, audio or video file or document the person clicked Save on;
// it lands in the media/file vault of the profile the request names, the vault
// `media list`, vault_item_list and vault_item_get_path read.
//
// The method only writes. Nothing here, and no other bridge method, reads the
// vault or returns a stored file: the reply names the item it made and no more.
// A request comes only from a paired extension socket (the token handshake in
// server_auth.go runs before any frame is read), and the extension sends this
// only from the card's click handler; the bridge cannot tell a click from a
// script, so that half is the extension's (SECURITY.md, "Saving to the vault
// from the extension").
//
// Like task.add, a save is idempotent by its client id: the extension keeps the
// id while it retries, and the vault answers an id it has seen with the item it
// already made. Every limit is checked here, at the boundary, before the sink
// opens the database; the sink checks that the profile is live. The vault lives
// in the host's database, so the host hands over a VaultSink
// (internal/cli/extensioncli/vault.go); a host with none does not advertise the
// method. Nothing here logs a payload.
//
//	extension -> Go  {"kind":"request","id":"req-...","method":"vault.save",
//	                  "params":{"client_id":"v-...","profile":"p-work","name":"invoice.pdf",
//	                            "mime":"application/pdf","data":"<base64>","source_url":"https://..."}}
//	Go -> extension  {"kind":"reply","id":"req-...","ok":true,
//	                  "data":{"id":"img-012","created":true,"name":"invoice.pdf","mime":"application/pdf","size_bytes":48213}}
//
// Refusals: invalid_input, quota_exceeded (data {limit, max}) and
// storage_conflict are final; rate_limited (data {retry_after}), unavailable
// and internal may be tried again.

// MethodVaultSave saves one file into a profile's vault.
const MethodVaultSave = "vault.save"

// Codes vault.save answers with beyond invalid_input, unavailable and
// internal. storage_conflict and quota_exceeded are final, like invalid_input:
// the same save is refused again until the person acts (checks the vault
// folder, deletes saved files). rate_limited is not: its data carries
// retry_after, the seconds until one more save fits.
const (
	CodeStorageConflict = "storage_conflict"
	CodeQuotaExceeded   = "quota_exceeded"
	CodeRateLimited     = "rate_limited"
)

// Limits on a vault.save, all checked before anything is written.
const (
	// VaultSaveMaxBytes caps the decoded file. A request is one WebSocket frame
	// and the bridge reads at most maxMessageSize (32 MiB) per frame, so the
	// base64 of 20 MiB (about 26.7 MiB) fits with room for the rest; it is
	// also the desktop app's cap on a chat attachment.
	VaultSaveMaxBytes = 20 << 20
	// vaultSaveMaxName caps the name in bytes, as most filesystems do.
	vaultSaveMaxName = 255
	// vaultSaveMaxClientID and vaultSaveMaxURL cap the other strings.
	vaultSaveMaxClientID = 128
	vaultSaveMaxURL      = 2048
)

// vaultSaveTypes are the media types vault.save takes, each with the extension
// its file is stored under. The bytes must match the type (vaultTypeMatches);
// the stored name is the item's id plus this extension, never the sender's name.
var vaultSaveTypes = map[string]string{
	"image/png":       ".png",
	"image/jpeg":      ".jpg",
	"image/gif":       ".gif",
	"image/webp":      ".webp",
	"application/pdf": ".pdf",
	"text/plain":      ".txt",
	"text/markdown":   ".md",
	"text/csv":        ".csv",
	"audio/mpeg":      ".mp3",
	"audio/wav":       ".wav",
	"audio/ogg":       ".ogg",
	"audio/mp4":       ".m4a",
	"video/mp4":       ".mp4",
	"video/webm":      ".webm",
}

// vaultSniffedAs maps a media type to what http.DetectContentType calls its
// bytes, where the two differ: a WAV sniffs as audio/wave, an Ogg stream as
// application/ogg, and an M4A is an MP4 container.
var vaultSniffedAs = map[string]string{
	"audio/wav": "audio/wave",
	"audio/ogg": "application/ogg",
	"audio/mp4": "video/mp4",
}

// VaultSaveMIME is the media type of a file vault.save stored, from its name
// (img-NNN<ext>); "" for an extension vault.save never writes.
func VaultSaveMIME(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	for mime, e := range vaultSaveTypes {
		if e == ext {
			return mime
		}
	}
	return ""
}

// VaultFile is a checked vault.save request, as the sink receives it.
type VaultFile struct {
	ProfileID string
	ClientID  string
	// Name is the label the item shows; it never becomes a path.
	Name string
	MIME string
	// Ext is the stored file's extension, from MIME.
	Ext  string
	Data []byte
	// SourceURL is the http(s) address the file came from as scheme, host and
	// path only (no user-info, query or fragment: a signed URL's signature is
	// in its query); "" when the request named none, or one of another scheme.
	SourceURL string
}

// VaultSaved is vault.save's reply: the item as stored, and whether this
// request made it (false: the client id was seen before and this is that item,
// with its own name, type and size, whatever this request sent). It never
// carries the file, its path or anything else read from the vault.
type VaultSaved struct {
	ID        string `json:"id"`
	Created   bool   `json:"created"`
	Name      string `json:"name"`
	MIME      string `json:"mime"`
	SizeBytes int    `json:"size_bytes"`
}

// VaultSink stores checked files. An error that must reach the extension with
// its code is a *RequestError (CodeInvalidInput for a profile that is gone,
// CodeUnavailable); any other error arrives as CodeInternal.
type VaultSink interface {
	SaveToVault(ctx context.Context, f VaultFile) (VaultSaved, error)
	// LiveProfiles lists the ids of the profiles that exist now. vault.save
	// asks at most once a second (liveProfileCache) and refuses an id not in
	// it before it counts the save or reads the file, so a made-up id costs
	// no rate bucket and no database work.
	LiveProfiles(ctx context.Context) ([]string, error)
}

// SetVaultSink installs the sink behind vault.save and registers the method
// with it; nil takes the method away again, as SetTaskSink does.
func (s *Server) SetVaultSink(sink VaultSink) {
	if sink == nil {
		s.handlerMu.Lock()
		delete(s.handlers, MethodVaultSave)
		s.handlerMu.Unlock()
		return
	}
	limiter := newSaveRateLimiter(vaultSaveRate, vaultSaveTotalRate, vaultSaveNow)
	s.vaultRate = limiter
	live := newLiveProfileCache(sink.LiveProfiles, vaultSaveNow)
	s.HandleRequest(MethodVaultSave, func(ctx context.Context, req *Request, _ ProgressFunc) (any, error) {
		// The cheap checks, then the profile must be live and the save must
		// fit the rate, and only then is the file decoded and checked.
		f, err := vaultFileHead(req)
		if err != nil {
			return nil, err
		}
		if ok, err := live.has(ctx, f.ProfileID); err != nil {
			return nil, err
		} else if !ok {
			// Also for a profile made within the last second, until the
			// list is read again (liveProfileCache).
			return nil, refusal(fmt.Sprintf("profile %.64q does not exist (any more): choose another in the side panel", f.ProfileID))
		}
		if wait, overall, ok := limiter.allow(f.ProfileID); !ok {
			secs := max(1, int((wait+time.Second-1)/time.Second))
			limit := fmt.Sprintf("at most %d saves a minute for one profile", vaultSaveRate)
			if overall {
				limit = fmt.Sprintf("at most %d saves a minute in all", vaultSaveTotalRate)
			}
			return nil, &RequestError{Code: CodeRateLimited, Data: map[string]any{"retry_after": secs},
				Err: fmt.Errorf("vault.save: %s; try again in %d s", limit, secs)}
		}
		if err := vaultFileBody(req, &f); err != nil {
			return nil, err
		}
		return sink.SaveToVault(ctx, f)
	})
}

// vaultFileHead reads and checks the cheap part of a vault.save request: who
// sent it, the profile and client ids, the name and the type. Every refusal
// here and in vaultFileBody is invalid_input: sending the same request again
// would be refused again.
func vaultFileHead(req *Request) (VaultFile, error) {
	if req.Origin.ConnectedAt.IsZero() {
		// serveRequest stamps every request with its paired socket; one
		// without came from no browser.
		return VaultFile{}, refusal("vault.save takes requests only from a paired browser")
	}
	f := VaultFile{
		ProfileID: req.String("profile"),
		ClientID:  req.String("client_id"),
		MIME:      strings.ToLower(req.String("mime")),
	}
	if i := strings.IndexByte(f.MIME, ';'); i >= 0 {
		f.MIME = strings.TrimSpace(f.MIME[:i])
	}
	switch {
	case f.ProfileID == "":
		return VaultFile{}, refusal(chooseProfileMessage)
	case !profiledir.ValidProfileID(f.ProfileID):
		return VaultFile{}, refusal(fmt.Sprintf("vault.save: %.64q is not a profile id", f.ProfileID))
	case f.ClientID == "":
		return VaultFile{}, refusal("vault.save needs a client_id, so that a retry cannot save a file twice")
	case !validClientID(f.ClientID):
		return VaultFile{}, refusal(fmt.Sprintf("vault.save: client_id must be 1-%d letters, digits or . _ : -", vaultSaveMaxClientID))
	}
	name, ok := req.Params["name"].(string)
	if !ok || !safeFileName(name) {
		return VaultFile{}, refusal(fmt.Sprintf("vault.save: the name must be a file name of at most %d bytes, without a folder, control characters, or . and ..", vaultSaveMaxName))
	}
	f.Name = name
	ext, ok := vaultSaveTypes[f.MIME]
	if !ok {
		return VaultFile{}, refusal(fmt.Sprintf("vault.save does not take %.64q; it takes PNG, JPEG, GIF and WebP images, MP3, WAV, Ogg and M4A audio, MP4 and WebM video, PDF, and plain text, Markdown and CSV", f.MIME))
	}
	f.Ext = ext
	return f, nil
}

// vaultFileBody decodes and checks the file itself, and its source address,
// into f (vaultFileHead's).
func vaultFileBody(req *Request, f *VaultFile) error {
	encoded, _ := req.Params["data"].(string)
	if encoded == "" {
		return refusal("vault.save needs the file's bytes, base64 in data")
	}
	if base64.StdEncoding.DecodedLen(len(encoded)) > VaultSaveMaxBytes+2 {
		return refusal(fmt.Sprintf("vault.save: the file is over the %d MiB limit", VaultSaveMaxBytes>>20))
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	switch {
	case err != nil:
		return refusal("vault.save: data is not base64")
	case len(data) == 0:
		return refusal("vault.save: the file is empty")
	case len(data) > VaultSaveMaxBytes:
		return refusal(fmt.Sprintf("vault.save: the file is over the %d MiB limit", VaultSaveMaxBytes>>20))
	case !vaultTypeMatches(f.MIME, data):
		return refusal(fmt.Sprintf("vault.save: the bytes are not %s", f.MIME))
	}
	f.Data = data
	f.SourceURL = cleanSourceURL(req.String("source_url"))
	return nil
}

// validClientID reports whether id is 1-vaultSaveMaxClientID of [A-Za-z0-9._:-].
func validClientID(id string) bool {
	if len(id) > vaultSaveMaxClientID {
		return false
	}
	for _, r := range id {
		if !(r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._:-", r))) {
			return false
		}
	}
	return id != ""
}

// safeFileName reports whether name is usable as a file's name on its own: not
// blank, at most vaultSaveMaxName bytes of valid UTF-8, without a path
// separator, control, format, line or paragraph separator character, and not
// "." or "..". It is only a label (the
// file is stored under the item's id), but a name that could name a folder or
// climb out of one is refused rather than repaired.
func safeFileName(name string) bool {
	if strings.TrimSpace(name) == "" || len(name) > vaultSaveMaxName || !utf8.ValidString(name) {
		return false
	}
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return false
	}
	for _, r := range name {
		if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) { // control, bidi/format (a name shown reversed), line and paragraph separators
			return false
		}
	}
	return true
}

// vaultTypeMatches reports whether data is what mime says, by its bytes
// (http.DetectContentType): an image, an audio or video file or a PDF must sniff as itself, and text
// must be UTF-8 that sniffs as plain text, so neither a script nor HTML is
// stored under another type's name.
func vaultTypeMatches(mime string, data []byte) bool {
	sniffed := http.DetectContentType(data)
	switch mime {
	case "text/plain", "text/markdown", "text/csv":
		return utf8.Valid(data) && strings.HasPrefix(sniffed, "text/plain")
	}
	if want, ok := vaultSniffedAs[mime]; ok {
		return sniffed == want
	}
	return sniffed == mime
}

// cleanSourceURL is raw as scheme, host and path of an absolute http(s)
// address, without user-info, query, fragment or a segment's ;parameters (a
// presigned URL's signature is in its query, a session id in ";jsessionid=",
// and the address shows in `media list` and the app), or "" for anything else
// (data:, blob:, javascript:, a relative path, a host with a ";", which no
// real host has): the address is a note on the item, so a bad one is
// dropped, not refused. A token that is a path segment itself is kept:
// nothing tells it from a file name.
func cleanSourceURL(raw string) string {
	if raw == "" || len(raw) > vaultSaveMaxURL {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsRune(u.Host, ';') {
		return ""
	}
	segs := strings.Split(u.EscapedPath(), "/")
	for i, s := range segs {
		if j := strings.IndexByte(s, ';'); j >= 0 {
			segs[i] = s[:j]
		}
	}
	rawPath := strings.Join(segs, "/")
	path, err := url.PathUnescape(rawPath)
	if err != nil {
		return ""
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: path, RawPath: rawPath}).String()
}
