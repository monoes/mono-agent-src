package httpnodes

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/monoes/mono-agent/internal/workflow"
)

// maxSFTPDownloadBytes caps a file http.ssh downloads into memory.
const maxSFTPDownloadBytes = 256 << 20 // 256 MiB

// checkSFTPConfig validates the resource "file" keys before dialling.
func checkSFTPConfig(config map[string]interface{}) error {
	op, _ := config["operation"].(string)
	if op != "upload" && op != "download" {
		return fmt.Errorf("http.ssh: 'operation' must be upload or download for resource file, got %q", op)
	}
	if p, _ := config["path"].(string); strings.TrimSpace(p) == "" {
		return fmt.Errorf("http.ssh: 'path' is required for file %s", op)
	}
	return nil
}

// sftpFiles runs a file upload or download over the SSH connection, once
// per input item (once with an empty item when there is no input).
//
// upload sends the item's binary property (binary_property, default
// "data") into the remote directory path, named file_name, else the
// item's file_name metadata, else the property name. download reads the
// remote file path into the binary property of a copy of the input item,
// with file_name (default the remote base name), mime_type and file_size.
func sftpFiles(ctx context.Context, client *ssh.Client, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	sc, err := sftp.NewClient(client)
	if err != nil {
		return nil, fmt.Errorf("http.ssh: sftp session failed: %w", err)
	}
	defer sc.Close()
	stop := context.AfterFunc(ctx, func() { sc.Close() })
	defer stop()

	op, _ := config["operation"].(string)
	remote := expandHome(sc, strings.TrimSpace(configString(config, "path")))
	prop := strings.TrimSpace(configString(config, "binary_property"))
	if prop == "" {
		prop = "data"
	}
	fileName := strings.TrimSpace(configString(config, "file_name"))

	items := input.Items
	if len(items) == 0 {
		items = []workflow.Item{workflow.NewItem(map[string]interface{}{})}
	}
	out := make([]workflow.Item, 0, len(items))
	for i, item := range items {
		var res workflow.Item
		if op == "upload" {
			res, err = sftpUpload(sc, item, remote, prop, fileName)
		} else {
			res, err = sftpDownload(sc, item, remote, prop, fileName)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("http.ssh: cancelled: %w", ctx.Err())
			}
			return nil, fmt.Errorf("http.ssh: item %d: %w", i, err)
		}
		out = append(out, res)
	}
	return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
}

// expandHome resolves a leading "~/" against the SFTP working directory,
// which is the login user's home on common servers.
func expandHome(sc *sftp.Client, p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	wd, err := sc.Getwd()
	if err != nil || wd == "" {
		return strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/")
	}
	return path.Join(wd, strings.TrimPrefix(p, "~"))
}

func sftpUpload(sc *sftp.Client, item workflow.Item, dir, prop, fileName string) (workflow.Item, error) {
	data, err := itemBinary(item, prop)
	if err != nil {
		return workflow.Item{}, err
	}
	if fileName == "" {
		fileName, _ = item.JSON["file_name"].(string)
	}
	if fileName == "" {
		fileName = prop
	}
	target := path.Join(dir, path.Base(fileName))
	f, err := sc.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return workflow.Item{}, fmt.Errorf("upload %s: %w", target, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return workflow.Item{}, fmt.Errorf("upload %s: %w", target, err)
	}
	if err := f.Close(); err != nil {
		return workflow.Item{}, fmt.Errorf("upload %s: %w", target, err)
	}
	return workflow.NewItem(map[string]interface{}{"success": true, "path": target, "file_size": len(data)}), nil
}

func sftpDownload(sc *sftp.Client, item workflow.Item, remote, prop, fileName string) (workflow.Item, error) {
	f, err := sc.Open(remote)
	if err != nil {
		return workflow.Item{}, fmt.Errorf("download %s: %w", remote, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSFTPDownloadBytes+1))
	if err != nil {
		return workflow.Item{}, fmt.Errorf("download %s: %w", remote, err)
	}
	if len(data) > maxSFTPDownloadBytes {
		return workflow.Item{}, fmt.Errorf("download %s: file is larger than %d bytes", remote, maxSFTPDownloadBytes)
	}
	if fileName == "" {
		fileName = path.Base(remote)
	}
	mt := mime.TypeByExtension(path.Ext(fileName))
	if mt == "" {
		mt = http.DetectContentType(data)
	}
	j := make(map[string]interface{}, len(item.JSON)+4)
	for k, v := range item.JSON {
		j[k] = v
	}
	j["file_name"], j["mime_type"], j["file_size"], j["path"] = fileName, mt, len(data), remote
	bin := make(map[string][]byte, len(item.Binary)+1)
	for k, v := range item.Binary {
		bin[k] = v
	}
	bin[prop] = data
	return workflow.Item{JSON: j, Binary: bin}, nil
}

// itemBinary returns binary property prop of an item: its bytes in
// Item.Binary, or a base64 string in the item's JSON.
func itemBinary(item workflow.Item, prop string) ([]byte, error) {
	if b, ok := item.Binary[prop]; ok {
		return b, nil
	}
	s, ok := item.JSON[prop].(string)
	if !ok {
		return nil, fmt.Errorf("binary_property: the input item has no binary data %q", prop)
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("binary_property: item field %q is not base64: %v", prop, err)
	}
	return b, nil
}
