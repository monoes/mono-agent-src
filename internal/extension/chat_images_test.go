package extension

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var (
	testPNG  = encodeTest(func(b *bytes.Buffer, m image.Image) { _ = png.Encode(b, m) })
	testJPEG = encodeTest(func(b *bytes.Buffer, m image.Image) { _ = jpeg.Encode(b, m, nil) })
)

func encodeTest(enc func(*bytes.Buffer, image.Image)) []byte {
	var b bytes.Buffer
	enc(&b, image.NewGray(image.Rect(0, 0, 4, 4)))
	return b.Bytes()
}

// pngClaiming is a PNG header that claims w x h pixels at 1 bit each.
func pngClaiming(w, h uint32) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8] = 1
	chunk := append([]byte("IHDR"), ihdr...)
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	_ = binary.Write(&b, binary.BigEndian, uint32(13))
	b.Write(chunk)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

func imageParam(mime string, data []byte) map[string]any {
	return map[string]any{"mime": mime, "data": base64.StdEncoding.EncodeToString(data)}
}

func sendWithImages(t *testing.T, f *fakeChat, images any) error {
	t.Helper()
	srv := chatSrv(f)
	_, err := callChat(t, srv, MethodChatSend, map[string]any{"profile": "default", "runtime": "claude", "message": "what is drawn?", "images": images}, nil)
	return err
}

// The images of a turn are written 0600 in a private folder for the runner,
// named by what they are (not by anything the page or the panel said), shown to
// it as --attach, fenced as untrusted in the message, and gone when the turn is.
func TestChatSendWritesImagesForTheTurnAndRemovesThem(t *testing.T) {
	var paths []string
	var contents []string
	var modes []os.FileMode
	var dirMode os.FileMode
	var message string
	f := &fakeChat{}
	f.onRun = func(s ChatTurnSpec) {
		message = s.Message
		for _, p := range s.Attach {
			paths = append(paths, p)
			b, err := os.ReadFile(p)
			if err != nil {
				t.Errorf("an attached image is not on disk during the turn: %v", err)
			}
			contents = append(contents, string(b))
			fi, _ := os.Stat(p)
			modes = append(modes, fi.Mode().Perm())
			di, _ := os.Stat(filepath.Dir(p))
			dirMode = di.Mode().Perm()
		}
	}
	if err := sendWithImages(t, f, []any{imageParam("image/jpeg", testJPEG), imageParam("image/png", testPNG)}); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || contents[0] != string(testJPEG) || contents[1] != string(testPNG) {
		t.Fatalf("paths %v contents %q", paths, contents)
	}
	if !strings.HasSuffix(paths[0], ".jpg") || !strings.HasSuffix(paths[1], ".png") {
		t.Errorf("names must come from the bytes: %v", paths)
	}
	for _, m := range modes {
		if runtime.GOOS != "windows" && m != 0o600 {
			t.Errorf("image mode %v, want 0600", m)
		}
	}
	if runtime.GOOS != "windows" && dirMode != 0o700 {
		t.Errorf("folder mode %v, want 0700", dirMode)
	}
	if !strings.Contains(message, "2 image") || !strings.Contains(message, "untrusted") {
		t.Errorf("the message does not fence the images: %q", message)
	}
	for _, p := range paths {
		if _, err := os.Stat(filepath.Dir(p)); !os.IsNotExist(err) {
			t.Errorf("the turn's image folder outlived the turn: %s", filepath.Dir(p))
		}
	}
}

func TestChatSendRemovesImagesWhenTheTurnFails(t *testing.T) {
	var dir string
	f := &fakeChat{runErr: errBusy()}
	f.onRun = func(s ChatTurnSpec) { dir = filepath.Dir(s.Attach[0]) }
	if err := sendWithImages(t, f, []any{imageParam("image/png", testPNG)}); err == nil {
		t.Fatal("expected the turn's error")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("a failed turn left its images: %s", dir)
	}
}

func TestChatSendRefusesBadImagesBeforeAnyTurn(t *testing.T) {
	big := append(append([]byte{}, testPNG...), make([]byte, chatMaxImageBytes)...)
	cases := map[string]any{
		"not a list":           "x",
		"entry not an object":  []any{"x"},
		"not base64":           []any{map[string]any{"mime": "image/png", "data": "!!!"}},
		"claims 60000x60000":   []any{imageParam("image/png", pngClaiming(60000, 60000))},
		"magic bytes disagree": []any{imageParam("image/png", testJPEG)},
		"not an image at all":  []any{imageParam("image/png", []byte("#!/bin/sh\nrm\n"))},
		"svg":                  []any{imageParam("image/svg+xml", []byte("<svg onload=x>"))},
		"too big":              []any{imageParam("image/png", big)},
		"too many":             []any{imageParam("image/png", testPNG), imageParam("image/png", testPNG), imageParam("image/png", testPNG), imageParam("image/png", testPNG)},
		"empty":                []any{imageParam("image/png", nil)},
	}
	for name, images := range cases {
		f := &fakeChat{}
		err := sendWithImages(t, f, images)
		if errCode(err) != CodeInvalidInput {
			t.Errorf("%s: err = %v, want invalid input", name, err)
		}
		if len(f.specs) != 0 || len(f.created) != 0 {
			t.Errorf("%s: a turn started anyway", name)
		}
	}
}

func TestChatTurnArgvPassesEachImage(t *testing.T) {
	argv := chatTurnArgv(ChatTurnSpec{Profile: "p", Conversation: "c", Turn: "t", Instance: "i", Attach: []string{"/x/a.png", "/x/b.jpg"}})
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--attach=/x/a.png") || !strings.Contains(joined, "--attach=/x/b.jpg") {
		t.Errorf("argv = %v", argv)
	}
	if strings.Contains(strings.Join(chatTurnArgv(ChatTurnSpec{Profile: "p", Conversation: "c", Turn: "t"}), " "), "--attach") {
		t.Error("a turn without images got --attach")
	}
}

func TestSweepChatImagesRemovesOnlyOldOwnFolders(t *testing.T) {
	root := t.TempDir()
	mk := func(name string, age time.Duration) string {
		p := filepath.Join(root, name)
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "image-1.png"), testPNG, 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-age)
		for _, q := range []string{filepath.Join(p, "image-1.png"), p} {
			if err := os.Chtimes(q, old, old); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}
	stale := mk("monoagent-chat-images-111", 2*time.Hour)
	fresh := mk("monoagent-chat-images-222", time.Minute)
	other := mk("something-else-333", 2*time.Hour)
	// A link named like ours, pointing at a folder that must survive.
	target := mk("target", 2*time.Hour)
	link := filepath.Join(root, "monoagent-chat-images-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	sweepChatImages(root, time.Hour, time.Now())
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the old folder is still there: %v", err)
	}
	for _, p := range []string{fresh, other, target, link} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s must be left alone: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(target, "image-1.png")); err != nil {
		t.Errorf("the link was followed: %v", err)
	}
}

func TestSweepChatImagesGoesByTheNewestFileInTheFolder(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "monoagent-chat-images-live")
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(p, "image-1.png")
	if err := os.WriteFile(f, testPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	sweepChatImages(root, chatImagesMaxAge, time.Now())
	if _, err := os.Stat(p); err != nil {
		t.Errorf("a folder with a fresh file was swept: %v", err)
	}
	if chatImagesMaxAge < 6*time.Hour {
		t.Errorf("chatImagesMaxAge = %v, want >= 6h", chatImagesMaxAge)
	}
}
