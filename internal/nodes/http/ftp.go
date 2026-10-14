package httpnodes

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	goftp "github.com/jlaffaye/ftp"
	"github.com/monoes/mono-agent/internal/fsconfine"
	"github.com/monoes/mono-agent/internal/workflow"
)

// FTPNode performs FTP operations.
// Type: "http.ftp"
type FTPNode struct{}

func (n *FTPNode) Type() string { return "http.ftp" }

func (n *FTPNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	operation, _ := config["operation"].(string)
	if operation == "" {
		return nil, fmt.Errorf("http.ftp: 'operation' is required")
	}

	host, _ := config["host"].(string)
	if host == "" {
		return nil, fmt.Errorf("http.ftp: 'host' is required")
	}

	port := 21
	if v, ok := config["port"].(float64); ok {
		port = int(v)
	}

	username, _ := config["username"].(string)
	password, _ := config["password"].(string)
	remotePath, _ := config["remote_path"].(string)
	oldPath, _ := config["old_path"].(string)
	newPath, _ := config["new_path"].(string)
	if operation == "rename" {
		if oldPath == "" || newPath == "" {
			return nil, fmt.Errorf("http.ftp: 'old_path' and 'new_path' are required for rename")
		}
	} else if remotePath == "" {
		return nil, fmt.Errorf("http.ftp: 'remote_path' is required")
	}
	localPath, _ := config["local_path"].(string)
	// C-46: confine the local side to the org workdir before dialling.
	if localPath != "" {
		var err error
		if localPath, err = fsconfine.Path(ctx, localPath); err != nil {
			return nil, fmt.Errorf("http.ftp: local_path: %w", err)
		}
	}

	addr := fmt.Sprintf("%s:%d", host, port)

	conn, err := goftp.Dial(addr, goftp.DialWithTimeout(30*time.Second), goftp.DialWithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("http.ftp: dial failed: %w", err)
	}
	defer conn.Quit()

	if username != "" {
		if err := conn.Login(username, password); err != nil {
			return nil, fmt.Errorf("http.ftp: login failed: %w", err)
		}
	}

	var resultItem workflow.Item

	switch operation {
	case "upload":
		if localPath == "" {
			return nil, fmt.Errorf("http.ftp: 'local_path' is required for upload")
		}
		f, err := os.Open(localPath)
		if err != nil {
			return nil, fmt.Errorf("http.ftp: cannot open local file: %w", err)
		}
		defer f.Close()
		if err := conn.Stor(remotePath, f); err != nil {
			return nil, fmt.Errorf("http.ftp: upload failed: %w", err)
		}
		resultItem = workflow.NewItem(map[string]interface{}{
			"success": true,
			"path":    remotePath,
		})

	case "download":
		r, err := conn.Retr(remotePath)
		if err != nil {
			return nil, fmt.Errorf("http.ftp: download failed: %w", err)
		}
		defer r.Close()

		data, err := io.ReadAll(r)
		if err != nil {
			return nil, fmt.Errorf("http.ftp: read failed: %w", err)
		}

		if localPath != "" {
			if err := os.WriteFile(localPath, data, 0o644); err != nil {
				return nil, fmt.Errorf("http.ftp: write local file failed: %w", err)
			}
			resultItem = workflow.NewItem(map[string]interface{}{
				"success":    true,
				"path":       remotePath,
				"local_path": localPath,
			})
		} else {
			resultItem = workflow.NewItem(map[string]interface{}{
				"success":  true,
				"path":     remotePath,
				"contents": base64.StdEncoding.EncodeToString(data),
			})
		}

	case "list":
		entries, err := conn.List(remotePath)
		if err != nil {
			return nil, fmt.Errorf("http.ftp: list failed: %w", err)
		}
		items := make([]interface{}, 0, len(entries))
		for _, e := range entries {
			items = append(items, map[string]interface{}{
				"name": e.Name,
				"type": e.Type.String(),
				"size": e.Size,
				"time": e.Time.Format(time.RFC3339),
			})
		}
		resultItem = workflow.NewItem(map[string]interface{}{
			"success": true,
			"path":    remotePath,
			"entries": items,
		})

	case "delete":
		if err := conn.Delete(remotePath); err != nil {
			// Try removing directory
			if err2 := conn.RemoveDirRecur(remotePath); err2 != nil {
				return nil, fmt.Errorf("http.ftp: delete failed: %w", err)
			}
		}
		resultItem = workflow.NewItem(map[string]interface{}{
			"success": true,
			"path":    remotePath,
		})

	case "rename":
		if createDirs, _ := config["create_directories"].(bool); createDirs {
			makeParentDirs(conn, newPath)
		}
		if err := conn.Rename(oldPath, newPath); err != nil {
			return nil, fmt.Errorf("http.ftp: rename failed: %w", err)
		}
		resultItem = workflow.NewItem(map[string]interface{}{
			"success":  true,
			"old_path": oldPath,
			"new_path": newPath,
		})

	default:
		return nil, fmt.Errorf("http.ftp: unknown operation %q", operation)
	}

	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{resultItem}}}, nil
}

// makeParentDirs creates the missing parent directories of p, one level at
// a time. Errors are ignored: a directory that already exists fails MKD,
// and a real problem surfaces when the operation itself fails.
func makeParentDirs(conn *goftp.ServerConn, p string) {
	dir := path.Dir(p)
	if dir == "." || dir == "/" {
		return
	}
	cur := ""
	if strings.HasPrefix(dir, "/") {
		cur = "/"
	}
	for _, part := range strings.Split(strings.Trim(dir, "/"), "/") {
		cur = path.Join(cur, part)
		_ = conn.MakeDir(cur)
	}
}
