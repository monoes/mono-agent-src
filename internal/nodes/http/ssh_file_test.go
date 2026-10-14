package httpnodes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net"
	"strings"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/monoes/mono-agent/internal/workflow"
)

// startSFTPServer runs an in-process SSH server on 127.0.0.1 that accepts
// user/secret and serves the sftp subsystem from an in-memory filesystem.
// It returns the port and a client of that filesystem for assertions.
func startSFTPServer(t *testing.T) (int, *sftp.Client) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
		if c.User() == "user" && string(pass) == "secret" {
			return nil, nil
		}
		return nil, ssh.ErrNoAuth
	}}
	cfg.AddHostKey(signer)

	handlers := sftp.InMemHandler()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSFTPConn(nc, cfg, handlers)
		}
	}()

	// A second client of the same in-memory filesystem, for test setup
	// and assertions.
	cliConn, srvConn := net.Pipe()
	srv := sftp.NewRequestServer(srvConn, handlers)
	go srv.Serve()
	sc, err := sftp.NewClientPipe(cliConn, cliConn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sc.Close(); srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port, sc
}

func serveSFTPConn(nc net.Conn, cfg *ssh.ServerConfig, handlers sftp.Handlers) {
	_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		if nch.ChannelType() != "session" {
			nch.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		ch, in, err := nch.Accept()
		if err != nil {
			return
		}
		go func() {
			for req := range in {
				ok := req.Type == "subsystem" && len(req.Payload) > 4 && string(req.Payload[4:]) == "sftp"
				req.Reply(ok, nil)
				if ok {
					srv := sftp.NewRequestServer(ch, handlers)
					srv.Serve()
					srv.Close()
				}
			}
		}()
	}
}

func sshFileConfig(port int, extra map[string]interface{}) map[string]interface{} {
	cfg := map[string]interface{}{"host": "127.0.0.1", "port": float64(port), "username": "user",
		"password": "secret", "resource": "file", "timeout_seconds": float64(10)}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

func TestSSHFile_UploadSendsEachItemsBinary(t *testing.T) {
	port, fs := startSFTPServer(t)
	if err := fs.Mkdir("/up"); err != nil {
		t.Fatal(err)
	}
	in := workflow.NodeInput{Items: []workflow.Item{
		{JSON: map[string]interface{}{"file_name": "a.txt"}, Binary: map[string][]byte{"doc": []byte("alpha")}},
		{JSON: map[string]interface{}{"file_name": "ignored.txt", "doc": base64.StdEncoding.EncodeToString([]byte("beta"))}},
	}}
	out, err := (&SSHNode{}).Execute(context.Background(), in, sshFileConfig(port, map[string]interface{}{
		"operation": "upload", "path": "/up", "binary_property": "doc"}))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	items := handleItems(out, "main")
	if len(items) != 2 || items[0].JSON["path"] != "/up/a.txt" || items[1].JSON["path"] != "/up/ignored.txt" {
		t.Fatalf("upload items = %+v", items)
	}
	for name, want := range map[string]string{"/up/a.txt": "alpha", "/up/ignored.txt": "beta"} {
		if got := readRemote(t, fs, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	// file_name on the node wins over the item's metadata.
	_, err = (&SSHNode{}).Execute(context.Background(), workflow.NodeInput{Items: in.Items[:1]}, sshFileConfig(port, map[string]interface{}{
		"operation": "upload", "path": "/up", "binary_property": "doc", "file_name": "renamed.bin"}))
	if err != nil {
		t.Fatalf("upload with file_name: %v", err)
	}
	if got := readRemote(t, fs, "/up/renamed.bin"); got != "alpha" {
		t.Errorf("renamed.bin = %q", got)
	}
}

func TestSSHFile_DownloadWritesBinaryProperty(t *testing.T) {
	port, fs := startSFTPServer(t)
	f, err := fs.Create("/report.csv")
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("a,b\n1,2\n"))
	f.Close()

	in := workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"keep": "me"})}}
	out, err := (&SSHNode{}).Execute(context.Background(), in, sshFileConfig(port, map[string]interface{}{
		"operation": "download", "path": "/report.csv", "binary_property": "file"}))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	items := handleItems(out, "main")
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	it := items[0]
	if string(it.Binary["file"]) != "a,b\n1,2\n" {
		t.Errorf("binary = %q", it.Binary["file"])
	}
	if it.JSON["keep"] != "me" || it.JSON["file_name"] != "report.csv" || it.JSON["file_size"] != 8 {
		t.Errorf("json = %+v", it.JSON)
	}
	if mt, _ := it.JSON["mime_type"].(string); mt == "" {
		t.Errorf("mime_type missing: %+v", it.JSON)
	}
}

func TestSSHFile_DownloadMissingFileNamesThePath(t *testing.T) {
	port, _ := startSFTPServer(t)
	_, err := (&SSHNode{}).Execute(context.Background(), workflow.NodeInput{}, sshFileConfig(port, map[string]interface{}{
		"operation": "download", "path": "/nope.txt"}))
	if err == nil || !strings.Contains(err.Error(), "/nope.txt") {
		t.Fatalf("err = %v, want one naming /nope.txt", err)
	}
}

func TestSSHFile_ValidatesBeforeDialling(t *testing.T) {
	for _, extra := range []map[string]interface{}{
		{"operation": "rename", "path": "/x"},
		{"operation": "upload"},
		{"operation": "download", "path": "  "},
	} {
		// Port 1 is never dialled: validation fails first.
		_, err := (&SSHNode{}).Execute(context.Background(), workflow.NodeInput{}, sshFileConfig(1, extra))
		if err == nil || strings.Contains(err.Error(), "dial") {
			t.Errorf("%v: err = %v, want a config error", extra, err)
		}
	}
	_, err := (&SSHNode{}).Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"host": "h", "username": "u", "password": "p", "resource": "disk"})
	if err == nil || !strings.Contains(err.Error(), "resource") {
		t.Errorf("unknown resource: err = %v", err)
	}
}

func readRemote(t *testing.T, fs *sftp.Client, name string) string {
	t.Helper()
	f, err := fs.Open(name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer f.Close()
	buf := make([]byte, 1024)
	n, _ := f.Read(buf)
	return string(buf[:n])
}
