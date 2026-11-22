//go:build unix

package openaiapi

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
)

// A runtime, or a prompt that steers it, can plant a link to an image anywhere on
// the disk. The gateway runs as the OS user, outside the sandbox: what the link
// points to is never read, however the request is made. Here end to end.
func TestImagesNeverAnswerWithWhatALinkPointsTo(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.png")
	secretBytes := []byte("\x89PNG\r\n\x1a\nthe contents of a file elsewhere on the disk")
	if err := os.WriteFile(secret, secretBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	plant := func(files map[string][]byte) execFunc {
		return func(ctx context.Context, o monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
			if err := os.Symlink(secret, filepath.Join(givenFolder(o), "a-image.png")); err != nil {
				return nil, err
			}
			if err := os.Symlink(outside, filepath.Join(givenFolder(o), "b-folder")); err != nil {
				return nil, err
			}
			return imageTurn("a-image.png", files)(ctx, o, onEvent)
		}
	}

	// Only links: the runtime made nothing the gateway may return.
	h := newHarness(t, plant(nil))
	rec := postImages(h, anyPolicy, h.key(t, "default", "app", false), `{"prompt":"x"}`)
	if rec.Code != http.StatusBadGateway || decodeErrorBody(t, rec)["code"] != "image_generation_failed" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String()[:min(rec.Body.Len(), 300)])
	}
	if strings.Contains(rec.Body.String(), base64.StdEncoding.EncodeToString(secretBytes)[:16]) {
		t.Fatal("the answer carries a file a link pointed to")
	}
	if lines := strings.Join(h.logged(), "\n"); !strings.Contains(lines, "a link x2") {
		t.Errorf("the log must say that links were left out: %q", lines)
	}

	// Next to a real image, the links are left out and the image is returned.
	h = newHarness(t, plant(map[string][]byte{"c-real.png": jpegBytes}))
	rec = postImages(h, anyPolicy, h.key(t, "default", "app", false), `{"prompt":"x","n":4}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if _, images := decodeImages(t, rec); !equalImages(images, jpegBytes) {
		t.Fatalf("got %d images, want only the real one", len(images))
	}
	if got, err := os.ReadFile(secret); err != nil || string(got) != string(secretBytes) {
		t.Errorf("the file behind the link was changed: %v", err)
	}
}

// A process a runtime left running keeps writing where it wrote before. The next turn of
// the same profile works in the same slot folder, and a process of the first one writes
// there, at the top of the folder and in the folder the first turn had: what it writes is
// not an image of the next turn, which is read from a folder that only that turn was told.
func TestImagesAProcessThatOutlivedAnEarlierTurnCannotAnswerTheNextOne(t *testing.T) {
	var turns atomic.Int32
	var mu sync.Mutex
	var procs []*exec.Cmd
	var firstFolder string
	h := newHarness(t, func(ctx context.Context, o monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
		poison := filepath.Join(o.Cwd, "0-poison.png")
		if turns.Add(1) == 1 {
			// The first turn makes its image, and leaves a process behind that writes a PNG
			// at the top of the working folder, and in the first turn's own folder (made again
			// when it is gone), for as long as it lives.
			firstFolder = givenFolder(o)
			const loop = `while :; do printf '\211PNG\r\n\032\nPOISON' > "$1/0-poison.png"; mkdir -p "$2" && printf '\211PNG\r\n\032\nPOISON' > "$2/0-poison.png"; sleep 0.02; done`
			cmd := exec.Command("sh", "-c", loop, "sh", o.Cwd, firstFolder)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				return nil, err
			}
			mu.Lock()
			procs = append(procs, cmd)
			mu.Unlock()
			return imageTurn("a.png", onePNG("a.png"))(ctx, o, onEvent)
		}
		// The second turn makes nothing, and replies once the process has written in both places.
		wrote := func() bool {
			for _, p := range []string{poison, filepath.Join(firstFolder, "0-poison.png")} {
				if _, err := os.Lstat(p); err != nil {
					return false
				}
			}
			return true
		}
		for end := time.Now().Add(10 * time.Second); !wrote() && time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		}
		if !wrote() {
			return nil, errors.New("the process of the first turn never wrote")
		}
		return imageTurn("I made nothing", nil)(ctx, o, onEvent)
	}, func(_ *Deps, c *Config) { c.MaxConcurrent = 1 })
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range procs {
			// The whole group: killing only the shell leaves its sleep, and the
			// loop's last write, to land in the folder as it is removed.
			_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
			_, _ = c.Process.Wait()
		}
	})

	if rec := postImages(h, anyPolicy, h.key(t, "default", "appA", false), `{"prompt":"x"}`); rec.Code != http.StatusOK {
		t.Fatalf("the first request: %d %s", rec.Code, rec.Body)
	}
	rec := postImages(h, anyPolicy, h.key(t, "default", "appB", false), `{"prompt":"x"}`)
	if rec.Code != http.StatusBadGateway || decodeErrorBody(t, rec)["code"] != "image_generation_failed" {
		t.Fatalf("the second request: %d %s: it made nothing, and what a process of the first one wrote is not its image", rec.Code, rec.Body.String()[:min(rec.Body.Len(), 300)])
	}
	if strings.Contains(rec.Body.String(), base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nPOISON"))[:16]) {
		t.Error("the answer carries what the process of the first turn wrote")
	}
}

// A FIFO standing where an image should be is never opened for reading, which
// would wait for a writer for ever: the request ends.
func TestImagesDoNotHangOnAFifo(t *testing.T) {
	h := newHarness(t, func(ctx context.Context, o monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
		if err := syscall.Mkfifo(filepath.Join(givenFolder(o), "image.png"), 0o600); err != nil {
			return nil, err
		}
		return imageTurn("image.png", nil)(ctx, o, onEvent)
	})
	secret := h.key(t, "default", "app", false)

	done := make(chan *httpRecorder, 1)
	go func() { done <- postImages(h, anyPolicy, secret, `{"prompt":"x"}`) }()
	select {
	case rec := <-done:
		if rec.Code != http.StatusBadGateway || decodeErrorBody(t, rec)["code"] != "image_generation_failed" {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the request hung on a FIFO")
	}
}
