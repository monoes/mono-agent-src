package browserctl

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/monoes/mono-agent/internal/extension"
)

// File uploads (`browser upload`): a file goes into a page's file input, or
// into whatever input a button opens the file chooser for, without the
// browser's own dialog. An agent's upload to a page that is not local waits
// for a person's approval exactly like a form submit does (Gated), as a
// Human-in-Loop item of Via ViaUpload that names every file with its size and
// digest; at full-no-approvals, and on local pages, it goes straight through.

// ViaUpload marks a held upload (SubmitItem.Via).
const ViaUpload = "upload"

// Limits on one upload: the files travel base64 over the bridge.
const (
	MaxUploadFiles = 10
	MaxUploadBytes = 20 << 20
)

// UploadFile is one file to upload, read once so what is approved and what is
// sent are the same bytes.
type UploadFile struct {
	Path   string
	Name   string
	Mime   string
	Size   int64
	SHA256 string
	data   []byte
}

// ReadUploadFiles reads the files at paths (~ expanded): regular files only,
// at most MaxUploadFiles and MaxUploadBytes together.
func ReadUploadFiles(paths []string) ([]UploadFile, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no file to upload")
	}
	if len(paths) > MaxUploadFiles {
		return nil, fmt.Errorf("at most %d files can be uploaded at once, not %d", MaxUploadFiles, len(paths))
	}
	var out []UploadFile
	var total int64
	for _, p := range paths {
		abs, err := uploadPath(p)
		if err != nil {
			return nil, err
		}
		if err := checkUploadAllowed(abs); err != nil {
			return nil, err
		}
		fi, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("upload: %w", err)
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("upload: %s is not a regular file", abs)
		}
		if total += fi.Size(); total > MaxUploadBytes {
			return nil, fmt.Errorf("upload: the files are over %d MB together", MaxUploadBytes>>20)
		}
		data, err := readUploadFile(abs, MaxUploadBytes-(total-fi.Size()))
		if err != nil {
			return nil, err
		}
		total += int64(len(data)) - fi.Size() // what was read, not what Stat said
		sum := sha256.Sum256(data)
		out = append(out, UploadFile{
			Path: abs, Name: filepath.Base(abs), Mime: uploadMime(abs, data), Size: int64(len(data)),
			SHA256: hex.EncodeToString(sum[:]), data: data,
		})
	}
	return out, nil
}

// readUploadFile reads the file at abs.
func readUploadFile(abs string, allowance int64) ([]byte, error) {
	f, err := os.Open(abs)
	if err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, allowance+1))
	if err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}
	if int64(len(data)) > allowance {
		return nil, fmt.Errorf("upload: the files are over %d MB together", MaxUploadBytes>>20)
	}
	return data, nil
}

func uploadPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("upload: an empty file path")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return filepath.Abs(p)
}

func uploadMime(path string, data []byte) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); t != "" {
		return t
	}
	return http.DetectContentType(data)
}

// Base64 is the file's content, base64.
func (f UploadFile) Base64() string { return base64.StdEncoding.EncodeToString(f.data) }

// UploadDigest binds an approval to what was shown: the target, the mode and
// every file's path and content.
func UploadDigest(selector string, chooser bool, files []UploadFile) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%t\n", selector, chooser)
	for _, f := range files {
		fmt.Fprintf(h, "%s\n%s\n", f.Path, f.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Upload puts files into the file input selector matches; with chooser,
// selector is the control that opens the file chooser (a styled button, a
// label) and the input it would open is found by clicking it. It returns how
// many files were set.
func (s *Session) Upload(selector string, chooser bool, files []UploadFile) (int, error) {
	type wire struct {
		Name     string `json:"name"`
		Data     string `json:"data"`
		MimeType string `json:"mimeType"`
	}
	fd := make([]wire, len(files))
	for i, f := range files {
		fd[i] = wire{Name: f.Name, Data: f.Base64(), MimeType: f.Mime}
	}
	params := map[string]interface{}{"selector": selector, "fileData": fd, "strict": true}
	if chooser {
		params["viaClick"] = true
	}
	var out struct {
		Count int `json:"count"`
	}
	if err := s.Call(extension.CmdSetFiles, params, 2*DefaultTimeout, &out); err != nil {
		return 0, uploadNoMatchHint(err)
	}
	return out.Count, nil
}

// uploadNoMatchHint adds what to do to the page's "nothing matched" answer.
// The extension reaches the top document only, so a file input inside an
// iframe is never found: it is not supported, and the way out is the page's
// own input in the top document (--chooser, with the control that opens it,
// works the same way). An extension older than the message says "Element not
// found" without the iframe note; that gets it here.
func uploadNoMatchHint(err error) error {
	msg := err.Error()
	if !strings.Contains(msg, "no element matched") && !strings.Contains(msg, "Element not found") {
		return err
	}
	if !strings.Contains(msg, "iframes are not supported") {
		msg += " (file inputs inside iframes are not supported)"
	}
	return fmt.Errorf("%s: upload needs a file input, or with --chooser the button that opens one, in the page's top document; select that one instead", msg)
}

// uploadSummary describes an upload for the approval card: one field per
// file, labelled with its full path and valued with its size and digest.
func uploadSummary(selector string, chooser bool, files []UploadFile) FormSummary {
	action := "input"
	if chooser {
		action = "chooser"
	}
	f := FormSummary{Action: action, Method: "UPLOAD", Selector: selector, Fields: []FormField{}}
	for _, u := range files {
		f.Fields = append(f.Fields, FormField{
			Name: u.Name, Type: "file", Label: u.Path,
			Value: fmt.Sprintf("%s, %d bytes, sha256 %s", u.Mime, u.Size, u.SHA256[:16]),
		})
	}
	return f
}

// HoldUpload records an upload that waits for approval as a Human-in-Loop
// item and returns it as an *AwaitingApproval.
func (s *Session) HoldUpload(ctx context.Context, db *sql.DB, selector string, chooser bool, files []UploadFile) (*AwaitingApproval, error) {
	it, err := CreateSubmitItem(ctx, db, SubmitRequest{
		Profile: s.profile, URL: s.url, Form: uploadSummary(selector, chooser, files), Via: ViaUpload, TabID: s.tabID,
		Instance: s.info.Instance, Digest: UploadDigest(selector, chooser, files),
	})
	if err != nil {
		return nil, err
	}
	if path := s.cardScreenshot(it.ID); path != "" {
		_ = setSubmitScreenshot(ctx, db, s.profile, it.ID, path)
	}
	return &AwaitingApproval{HILID: it.ID, Origin: it.Origin, URL: it.URL, Via: ViaUpload, Profile: s.profile}, nil
}

// UploadFromItem rereads the files an approved upload item names and checks
// them, the target and the page against what was approved. Its errors wrap
// ErrNotSent: nothing reached the page.
func UploadFromItem(p *extension.ExtensionPage, it SubmitItem) (selector string, chooser bool, files []UploadFile, err error) {
	if it.Via != ViaUpload {
		return "", false, nil, fmt.Errorf("%w: item %s is not an upload (use `browser submit --hil %s`)", ErrNotSent, it.ID, it.ID)
	}
	if it.FormDigest == "" {
		return "", false, nil, ErrSubmitUnbound
	}
	now, err := p.GetURL()
	if err != nil {
		return "", false, nil, fmt.Errorf("%w: reading the tab: %v", ErrNotSent, err)
	}
	if pagePath(now) != pagePath(it.URL) {
		return "", false, nil, &SubmitChangedError{What: fmt.Sprintf("the page (approved on %s, now %s)", pagePath(it.URL), pagePath(now))}
	}
	selector, chooser = it.Form.Selector, it.Form.Action == "chooser"
	paths := make([]string, len(it.Form.Fields))
	for i, f := range it.Form.Fields {
		paths[i] = f.Label
	}
	files, err = ReadUploadFiles(paths)
	if err != nil {
		return "", false, nil, fmt.Errorf("%w: %v", ErrNotSent, err)
	}
	if UploadDigest(selector, chooser, files) != it.FormDigest {
		return "", false, nil, &SubmitChangedError{What: "a file"}
	}
	return selector, chooser, files, nil
}
