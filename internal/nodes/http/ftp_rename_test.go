package httpnodes

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// fakeFTP is a control-connection-only FTP server: enough for login,
// MKD and RNFR/RNTO. It records every command it receives.
type fakeFTP struct {
	mu   sync.Mutex
	cmds []string
	port int
}

func startFakeFTP(t *testing.T, failRename bool) *fakeFTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeFTP{port: ln.Addr().(*net.TCPAddr).Port}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c, failRename)
		}
	}()
	return f
}

func (f *fakeFTP) serve(c net.Conn, failRename bool) {
	defer c.Close()
	r := bufio.NewReader(c)
	reply := func(s string) { fmt.Fprintf(c, "%s\r\n", s) }
	reply("220 fake ready")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		f.mu.Lock()
		f.cmds = append(f.cmds, line)
		f.mu.Unlock()
		verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch verb {
		case "USER":
			reply("331 password please")
		case "PASS":
			reply("230 logged in")
		case "FEAT":
			reply("211 no features")
		case "TYPE", "OPTS":
			reply("200 ok")
		case "MKD":
			reply("257 created")
		case "RNFR":
			reply("350 ready for RNTO")
		case "RNTO":
			if failRename {
				reply("550 rename refused")
			} else {
				reply("250 renamed")
			}
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 not implemented")
		}
	}
}

func (f *fakeFTP) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

func TestFTPNode_Rename(t *testing.T) {
	srv := startFakeFTP(t, false)
	out, err := (&FTPNode{}).Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"host": "127.0.0.1", "port": float64(srv.port), "username": "u", "password": "p",
		"operation": "rename", "old_path": "/in/a.csv", "new_path": "/archive/2026/a.csv", "create_directories": true,
	})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	items := handleItems(out, "main")
	if len(items) != 1 || items[0].JSON["old_path"] != "/in/a.csv" || items[0].JSON["new_path"] != "/archive/2026/a.csv" {
		t.Fatalf("items = %+v", items)
	}
	got := strings.Join(srv.commands(), "\n")
	for _, want := range []string{"MKD /archive", "MKD /archive/2026", "RNFR /in/a.csv", "RNTO /archive/2026/a.csv"} {
		if !strings.Contains(got, want) {
			t.Errorf("commands missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "MKD /archive/2026") > strings.Index(got, "RNFR") {
		t.Errorf("directories must be made before renaming:\n%s", got)
	}
}

func TestFTPNode_RenameErrors(t *testing.T) {
	srv := startFakeFTP(t, true)
	base := map[string]interface{}{"host": "127.0.0.1", "port": float64(srv.port), "operation": "rename"}
	if _, err := (&FTPNode{}).Execute(context.Background(), workflow.NodeInput{}, base); err == nil ||
		!strings.Contains(err.Error(), "old_path") {
		t.Errorf("missing paths: err = %v", err)
	}
	base["old_path"], base["new_path"] = "/a", "/b"
	_, err := (&FTPNode{}).Execute(context.Background(), workflow.NodeInput{}, base)
	if err == nil || !strings.Contains(err.Error(), "rename failed") {
		t.Errorf("refused rename: err = %v", err)
	}
	if strings.Contains(strings.Join(srv.commands(), "\n"), "MKD") {
		t.Errorf("MKD sent without create_directories: %v", srv.commands())
	}
}
