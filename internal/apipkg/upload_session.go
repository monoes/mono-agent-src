package apipkg

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// UploadSession configures chunked uploads of large files through a
// resumable session: Create opens it (an op step, sent with auth; its
// response holds the upload URL at URLPath), then the file is PUT to that
// URL in ChunkSize pieces with Content-Range headers and no credential (the
// URL is pre-authorised). The last chunk's response is the op's response.
type UploadSession struct {
	// MinSize: files larger than this many bytes use the session; smaller
	// ones go in one request as before. Default 4 MiB.
	MinSize int64 `json:"min_size,omitempty"`
	// ChunkSize is the bytes per PUT. Default 10 MiB (Graph wants a
	// multiple of 320 KiB).
	ChunkSize int64 `json:"chunk_size,omitempty"`
	// URLPath is the dot path of the upload URL in Create's response
	// (default "uploadUrl").
	URLPath string `json:"url_path,omitempty"`
	Create  Step   `json:"create"`
}

const (
	defaultSessionMinSize   = 4 << 20
	defaultSessionChunkSize = 10 << 20
)

// sendMain sends the op's main request: one request, or, for a large
// upload with binary.upload_session, the session's chunks.
func (r *opRun) sendMain(ctx context.Context) (*response, error) {
	s := r.op.Binary
	if s == nil || s.UploadSession == nil || !r.uploads() {
		return r.send(ctx, pageParams{})
	}
	us := s.UploadSession
	data, err := r.uploadData()
	if err != nil {
		return nil, err
	}
	minSize := us.MinSize
	if minSize <= 0 {
		minSize = defaultSessionMinSize
	}
	if int64(len(data)) <= minSize {
		return r.send(ctx, pageParams{})
	}
	created, err := r.sendStep(ctx, us.Create, r.scope)
	if err != nil {
		return nil, fmt.Errorf("binary.upload_session.create: %w", err)
	}
	if created == nil {
		return nil, r.errf("binary.upload_session.create: skipped by its when")
	}
	path := us.URLPath
	if path == "" {
		path = "uploadUrl"
	}
	uploadURL := stringify(lookupPath(decodeBody(created.body), strings.Split(path, ".")))
	if !strings.HasPrefix(uploadURL, "http://") && !strings.HasPrefix(uploadURL, "https://") {
		return nil, r.errf("binary.upload_session: the create response has no upload URL at %q", path)
	}
	chunk := us.ChunkSize
	if chunk <= 0 {
		chunk = defaultSessionChunkSize
	}
	total := int64(len(data))
	var last *response
	for start := int64(0); start < total; start += chunk {
		end := min(start+chunk, total)
		if last, err = r.putChunk(ctx, uploadURL, data[start:end], start, total); err != nil {
			return nil, err
		}
	}
	return last, nil
}

// putChunk sends one piece of a session upload.
func (r *opRun) putChunk(ctx context.Context, uploadURL string, part []byte, start, total int64) (*response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(part))
	if err != nil {
		return nil, r.errf("upload session: %w", err)
	}
	req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+int64(len(part))-1, total))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := r.client().Do(req)
	if err != nil {
		return nil, r.errf("upload session chunk at byte %d: %w", start, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return nil, r.errf("upload session chunk at byte %d: reading response: %w", start, err)
	}
	if len(body) > maxResponseBody {
		return nil, r.errf("response body exceeds %d MiB", maxResponseBody>>20)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, r.errf("upload session chunk at byte %d: HTTP %d: %s", start, resp.StatusCode, errorBodyEcho(body))
	}
	return &response{status: resp.StatusCode, header: resp.Header, body: body, url: uploadURL}, nil
}
