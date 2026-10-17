//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestUploadBatchOnlyUploadsIssuedRegularFiles(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.txt")
	forged := filepath.Join(dir, "forged.txt")
	link := filepath.Join(dir, "link.txt")
	fifo := filepath.Join(dir, "fifo.txt")
	for _, p := range []string{good, forged} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(good, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skip("mkfifo unavailable:", err)
	}

	a := &App{}
	a.issueDocPaths([]string{good, link, fifo}) // forged was never issued
	var uploaded []string
	res, err := a.uploadBatch([]string{good, forged, link, fifo}, func(_ context.Context, p string) (UploadResult, error) {
		uploaded = append(uploaded, p)
		return UploadResult{Indexed: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(uploaded) != 1 || uploaded[0] != good || res.Uploaded != 1 || len(res.Failed) != 3 {
		t.Fatalf("uploaded=%v res=%+v", uploaded, res)
	}
	if a.docBatchBusy.Load() {
		t.Fatal("busy flag not released")
	}
	// The issued set is consumed: a second batch with the same path is refused.
	res, _ = a.uploadBatch([]string{good}, func(context.Context, string) (UploadResult, error) {
		t.Fatal("uploaded a path from a consumed set")
		return UploadResult{}, nil
	})
	if len(res.Failed) != 1 {
		t.Fatalf("res=%+v", res)
	}
}
