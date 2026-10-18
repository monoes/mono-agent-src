package hilsend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Run is what Resolve asks a Runner to perform: the original node, with the
// exact previewed payload (edits applied). ApprovalID and Proof go into the
// performing process's environment (EnvApprovalID, EnvApprovalProof) so Guard
// lets that one send through.
type Run struct {
	NodeType   string
	Config     map[string]interface{}
	Input      []map[string]interface{}
	ApprovalID string
	Proof      string
	PreparedBy PreparedBy
}

// Runner performs a Run and returns the node's output. The CLI's runner starts
// `monoagentcli node run`, so browser sessions, credentials and the
// publication registration work as they do for any node run; tests inject a
// fake.
type Runner func(ctx context.Context, r Run) (json.RawMessage, error)

// Edit overrides the text and/or subject before approving (nil keeps it).
type Edit struct {
	Text, Subject *string
}

// Resolve approves or rejects a send item.
//
// Approving performs the send exactly once: the item moves from pending to
// approved with a single atomic update, so a second Resolve (a double click,
// a retry) finds it no longer pending and returns its current state without
// running anything. A failed send is final too: the item stays failed with its
// error, and sending again means preparing a new item.
// Rejecting never runs anything. always (approve only) also remembers the
// destination so later sends to it from an agent go out without a card.
func Resolve(ctx context.Context, st Store, id string, approve bool, edit Edit, always bool, run Runner) (Payload, error) {
	if !approve {
		p, _, err := st.reject(ctx, id)
		return p, err
	}
	if always {
		// Before the edit and the claim: the item stays pending, as it was,
		// for a plain approve. claim checks again on what it claims.
		p, err := st.Get(ctx, id)
		if err != nil {
			return p, err
		}
		if p.Recipient.Label {
			return p, ErrAlwaysLabel
		}
	}
	if edit.Text != nil || edit.Subject != nil {
		// A non-pending item can't be edited; claim below reports its state.
		// A card with no text to edit is not approved with the edit dropped:
		// nothing is sent, and the item stays pending.
		if p, err := st.Edit(ctx, id, edit.Text, edit.Subject); errors.Is(err, ErrNotFound) {
			return Payload{}, err
		} else if errors.Is(err, ErrNoText) {
			return p, err
		}
	}
	p, rp, proof, claimed, err := st.claim(ctx, id, always)
	if err != nil || !claimed {
		return p, err
	}
	if always {
		if err := st.Allow(ctx, Allow{Action: p.Action, Platform: p.Account.Platform, Account: p.Account.Handle, Recipient: keyOf(p.Recipient)}); err != nil {
			return st.finishErr(ctx, p.ID, err)
		}
	}
	cfg := cloneMap(rp.Config)
	if rp.TextField != "" {
		setAt(cfg, rp.TextField, p.Text)
	}
	if rp.SubjectKey != "" && p.Subject != "" {
		setAt(cfg, rp.SubjectKey, p.Subject)
	}
	result, runErr := run(ctx, Run{NodeType: rp.NodeType, Config: cfg, Input: rp.Input, ApprovalID: p.ID, Proof: proof, PreparedBy: p.PreparedBy})
	out, err := st.finish(ctx, p.ID, result, runErr)
	if err != nil {
		return out, err
	}
	out.AlwaysAllowed = always
	return out, nil
}

func (s Store) finishErr(ctx context.Context, id string, cause error) (Payload, error) {
	p, err := s.finish(ctx, id, nil, cause)
	if err != nil {
		return p, err
	}
	return p, nil
}

// File is an attachment resolved to a readable local file.
type File struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Mime string `json:"mime,omitempty"`
	Size int64  `json:"size"`
	Kind string `json:"kind"`
}

// MaxPreviewBytes bounds what the preview command serves.
const MaxPreviewBytes = 50 << 20

// ErrOutsideRoots: the file is not under a folder the preview may read.
var ErrOutsideRoots = errors.New("attachment is outside the folders a preview may read")

// AttachmentFile resolves attachment n of p to a local file that lies under
// one of roots (the profile folder, the downloads folder, the chat folder).
// Relative paths are taken from the first root. Symbolic links are followed
// before the check, so a link cannot lead out; a path with .. cannot either.
func AttachmentFile(p Payload, n int, roots []string) (File, error) {
	if n < 0 || n >= len(p.Attachments) {
		return File{}, fmt.Errorf("attachment %d: this item has %d", n, len(p.Attachments))
	}
	a := p.Attachments[n]
	if a.Path == "" {
		return File{}, fmt.Errorf("attachment %d is a link (%s), not a file", n, a.URL)
	}
	var real []string
	for _, r := range roots {
		if r == "" {
			continue
		}
		if abs, err := filepath.EvalSymlinks(r); err == nil {
			real = append(real, abs)
		}
	}
	if len(real) == 0 {
		return File{}, ErrOutsideRoots
	}
	path := a.Path
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(real[0], path)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return File{}, fmt.Errorf("attachment %d: %w", n, err)
	}
	inside := false
	for _, r := range real {
		if rel, err := filepath.Rel(r, resolved); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			inside = true
		}
	}
	if !inside {
		return File{}, ErrOutsideRoots
	}
	st, err := os.Stat(resolved)
	if err != nil {
		return File{}, err
	}
	if !st.Mode().IsRegular() {
		return File{}, fmt.Errorf("attachment %d is not a regular file", n)
	}
	if st.Size() > MaxPreviewBytes {
		return File{}, fmt.Errorf("attachment %d is %d bytes, over the %d byte preview limit", n, st.Size(), MaxPreviewBytes)
	}
	return File{Path: resolved, Name: a.Name, Mime: a.Mime, Size: st.Size(), Kind: a.Kind}, nil
}
