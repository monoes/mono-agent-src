package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/monoes/mono-agent/internal/statedir"
)

// WorkflowFile is the on-disk JSON representation of a workflow.
type WorkflowFile struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Version     int                `json:"version"`
	IsActive    bool               `json:"is_active"`
	ProfileID   string             `json:"profile_id,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
	Nodes       []WorkflowFileNode `json:"nodes"`
	Connections []WorkflowFileEdge `json:"connections"`

	// Settings are workflow-level execution options (Workflow.Settings).
	Settings map[string]interface{} `json:"settings,omitempty"`
}

// WorkflowFileNode is a node as stored in the JSON file.
type WorkflowFileNode struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Position struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	} `json:"position"`
	Disabled bool                   `json:"disabled"`
	Config   map[string]interface{} `json:"config"`
	Schema   *NodeSchema            `json:"schema"`
}

// WorkflowFileEdge is a connection as stored in the JSON file.
type WorkflowFileEdge struct {
	ID           string `json:"id"`
	Source       string `json:"source"`
	SourceHandle string `json:"source_handle"`
	Target       string `json:"target"`
	TargetHandle string `json:"target_handle"`
}

// fileCacheEntry caches one parsed workflow keyed by its file identity
// (modtime + size), so ListWorkflows skips re-parsing unchanged files.
type fileCacheEntry struct {
	modTime time.Time
	size    int64
	wf      *Workflow
}

// WorkflowFileStore implements workflow CRUD using JSON files.
// Does NOT handle executions or credentials — use SQLiteWorkflowStore for those.
type WorkflowFileStore struct {
	dir string

	mu    sync.Mutex
	cache map[string]fileCacheEntry
}

// NewWorkflowFileStore creates a WorkflowFileStore backed by the given directory.
// The directory is created if it does not exist.
func NewWorkflowFileStore(dir string) (*WorkflowFileStore, error) {
	if err := statedir.MkdirAll(dir); err != nil {
		return nil, fmt.Errorf("file_store: create dir %s: %w", dir, err)
	}
	return &WorkflowFileStore{dir: dir, cache: make(map[string]fileCacheEntry)}, nil
}

// filePath maps a workflow ID to its on-disk JSON file. The ID must be a plain
// filename component: IDs containing path separators, "..", or that resolve to
// anything other than themselves are rejected to prevent path traversal outside
// the store directory (e.g. an imported workflow whose "id" is "../../etc/foo").
func (s *WorkflowFileStore) filePath(id string) (string, error) {
	if id == "" || id == "." || id == ".." ||
		strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return "", fmt.Errorf("file_store: invalid workflow id %q", id)
	}
	return filepath.Join(s.dir, id+".json"), nil
}

// SaveWorkflow writes or updates a workflow JSON file.
// Assigns a new UUID if wf.ID is empty.
// Embeds default schema for any node missing its Schema field.
func (s *WorkflowFileStore) SaveWorkflow(ctx context.Context, wf *Workflow) error {
	fileWriteMu.Lock()
	defer fileWriteMu.Unlock()
	return s.saveWorkflow(ctx, wf)
}

// saveWorkflowDurable is kept for the guarded save; every save is atomic and flushed now.
func (s *WorkflowFileStore) saveWorkflowDurable(ctx context.Context, wf *Workflow) error {
	return s.saveWorkflow(ctx, wf)
}

func (s *WorkflowFileStore) saveWorkflow(ctx context.Context, wf *Workflow) error {
	if wf.ID == "" {
		wf.ID = uuid.New().String()
		wf.CreatedAt = time.Now().UTC()
	}
	wf.UpdatedAt = time.Now().UTC()
	if wf.Version == 0 {
		wf.Version = 1
	}

	wfFile := WorkflowFile{
		ID:          wf.ID,
		Name:        wf.Name,
		Description: wf.Description,
		Version:     wf.Version,
		IsActive:    wf.IsActive,
		ProfileID:   wf.ProfileID,
		CreatedAt:   wf.CreatedAt,
		UpdatedAt:   wf.UpdatedAt,
		Settings:    wf.Settings,
	}

	for _, n := range wf.Nodes {
		fn := WorkflowFileNode{
			ID:       n.ID,
			Type:     n.Type,
			Name:     n.Name,
			Disabled: n.Disabled,
			Config:   n.Config,
			Schema:   n.Schema,
		}
		fn.Position.X = n.PositionX
		fn.Position.Y = n.PositionY
		if fn.Config == nil {
			fn.Config = map[string]interface{}{}
		}
		if fn.Schema == nil {
			schema, err := LoadDefaultSchema(n.Type)
			if err == nil {
				fn.Schema = schema
			}
		}
		wfFile.Nodes = append(wfFile.Nodes, fn)
	}

	NormalizeConnectionHandles(wf.Connections)
	for _, c := range wf.Connections {
		wfFile.Connections = append(wfFile.Connections, WorkflowFileEdge{
			ID:           c.ID,
			Source:       c.SourceNodeID,
			SourceHandle: c.SourceHandle,
			Target:       c.TargetNodeID,
			TargetHandle: c.TargetHandle,
		})
	}

	data, err := json.MarshalIndent(wfFile, "", "  ")
	if err != nil {
		return fmt.Errorf("file_store: marshal %s: %w", wf.ID, err)
	}
	path, err := s.filePath(wf.ID)
	if err != nil {
		return err
	}
	// Write atomically: write to a temp file then rename over the target, so a
	// crash mid-write cannot truncate/corrupt an existing workflow definition.
	if err := writeFileAtomic(path, data); err != nil {
		return fmt.Errorf("file_store: write %s: %w", wf.ID, err)
	}
	s.invalidate(path)
	return nil
}

// writeFileAtomic replaces path with data so that a reader sees the old file or the new one and a
// crash leaves one of them: the bytes go to a RANDOM temp file created exclusively in the same
// folder (O_EXCL, mode 0600, so a pre-planted file or symlink is never followed or truncated),
// are flushed, the temp file is renamed over path, and the folder is flushed. The temp file is
// removed on every failure.
func writeFileAtomic(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err := statedir.MkdirAll(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(dir)
}

// syncDir flushes a directory's entries (the rename) to disk. Platforms that cannot open a
// directory for this (Windows) keep the rename as it is.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}

// readRaw returns the workflow file's bytes as they are on disk, and whether
// the file exists — what restoreRaw needs to undo a SaveWorkflow.
func (s *WorkflowFileStore) readRaw(id string) ([]byte, bool, error) {
	path, err := s.filePath(id)
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("file_store: read %s: %w", id, err)
	}
	return data, true, nil
}

// restoreRaw puts back a workflow file captured by readRaw: the previous
// bytes when it existed, no file when it did not.
func (s *WorkflowFileStore) restoreRaw(id string, data []byte, existed bool) error {
	path, err := s.filePath(id)
	if err != nil {
		return err
	}
	defer s.invalidate(path)
	if !existed {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("file_store: remove %s: %w", id, err)
		}
		return syncDir(filepath.Dir(path))
	}
	if err := writeFileAtomic(path, data); err != nil {
		return fmt.Errorf("file_store: write %s: %w", id, err)
	}
	return nil
}

// GetWorkflow reads a workflow JSON file by ID.
// Returns nil, nil if not found.
func (s *WorkflowFileStore) GetWorkflow(ctx context.Context, id string) (*Workflow, error) {
	path, err := s.filePath(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("file_store: read %s: %w", id, err)
	}
	return parseWorkflowFile(data)
}

// ListWorkflows scans the directory and returns all workflows sorted by
// UpdatedAt desc. Parsed workflows are cached per file and reused while the
// file's modtime and size are unchanged.
func (s *WorkflowFileStore) ListWorkflows(ctx context.Context) ([]*Workflow, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("file_store: readdir %s: %w", s.dir, err)
	}
	var wfs []*Workflow
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		wf, err := s.cachedWorkflow(e)
		if err != nil {
			continue
		}
		wfs = append(wfs, wf)
	}
	sort.Slice(wfs, func(i, j int) bool {
		return wfs[i].UpdatedAt.After(wfs[j].UpdatedAt)
	})
	return wfs, nil
}

// cachedWorkflow returns the parsed workflow for a directory entry, serving
// it from the cache when the file's modtime and size match the cached entry.
func (s *WorkflowFileStore) cachedWorkflow(e os.DirEntry) (*Workflow, error) {
	path := filepath.Join(s.dir, e.Name())
	info, err := e.Info()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	c, ok := s.cache[path]
	s.mu.Unlock()
	if ok && c.size == info.Size() && c.modTime.Equal(info.ModTime()) {
		return c.wf, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	wf, err := parseWorkflowFile(data)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cache[path] = fileCacheEntry{modTime: info.ModTime(), size: info.Size(), wf: wf}
	s.mu.Unlock()
	return wf, nil
}

// invalidate drops the cached parse for path (called on save/delete, where
// the on-disk file has just been rewritten or removed).
func (s *WorkflowFileStore) invalidate(path string) {
	s.mu.Lock()
	delete(s.cache, path)
	s.mu.Unlock()
}

// DeleteWorkflow removes the workflow JSON file.
func (s *WorkflowFileStore) DeleteWorkflow(ctx context.Context, id string) error {
	path, err := s.filePath(id)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err == nil {
		s.invalidate(path)
	}
	return err
}

// ParseWorkflowFileBytes is the exported counterpart of parseWorkflowFile.
// It converts raw workflow-file JSON (using "type", "source", "target" keys)
// into a *Workflow. Used by the import command.
func ParseWorkflowFileBytes(data []byte) (Workflow, error) {
	wf, err := parseWorkflowFile(data)
	if err != nil {
		return Workflow{}, err
	}
	return *wf, nil
}

// parseWorkflowFile converts raw JSON bytes into a *Workflow.
func parseWorkflowFile(data []byte) (*Workflow, error) {
	var wfFile WorkflowFile
	if err := json.Unmarshal(data, &wfFile); err != nil {
		return nil, fmt.Errorf("file_store: unmarshal: %w", err)
	}
	wf := &Workflow{
		ID:          wfFile.ID,
		Name:        wfFile.Name,
		Description: wfFile.Description,
		Version:     wfFile.Version,
		IsActive:    wfFile.IsActive,
		ProfileID:   wfFile.ProfileID,
		CreatedAt:   wfFile.CreatedAt,
		UpdatedAt:   wfFile.UpdatedAt,
		Settings:    wfFile.Settings,
	}
	for _, fn := range wfFile.Nodes {
		n := WorkflowNode{
			ID:         fn.ID,
			WorkflowID: wf.ID,
			Type:       fn.Type,
			Name:       fn.Name,
			PositionX:  fn.Position.X,
			PositionY:  fn.Position.Y,
			Disabled:   fn.Disabled,
			Config:     fn.Config,
			Schema:     fn.Schema,
		}
		if n.Config == nil {
			n.Config = map[string]interface{}{}
		}
		wf.Nodes = append(wf.Nodes, n)
	}
	for _, fe := range wfFile.Connections {
		wf.Connections = append(wf.Connections, WorkflowConnection{
			ID:           fe.ID,
			WorkflowID:   wf.ID,
			SourceNodeID: fe.Source,
			SourceHandle: fe.SourceHandle,
			TargetNodeID: fe.Target,
			TargetHandle: fe.TargetHandle,
		})
	}
	NormalizeConnectionHandles(wf.Connections)
	return wf, nil
}

// NormalizeConnectionHandles defaults empty connection handles to "main",
// the port every node without named outputs reads and writes. A workflow
// file may leave source_handle/target_handle out; the engine routes only on
// exact handle names, so an empty one would silently skip its target.
func NormalizeConnectionHandles(conns []WorkflowConnection) {
	for i := range conns {
		if conns[i].SourceHandle == "" {
			conns[i].SourceHandle = "main"
		}
		if conns[i].TargetHandle == "" {
			conns[i].TargetHandle = "main"
		}
	}
}
