//go:build !windows

package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// pngBytes is enough of a PNG for content sniffing.
var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01")

func mustWrite(t *testing.T, path string, data []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWorkspaceImageIsConfinedToTheFolder(t *testing.T) {
	cwd, outside := t.TempDir(), t.TempDir()
	shot := mustWrite(t, filepath.Join(cwd, ".monoagent", "screenshots", "a.png"), pngBytes)
	mustWrite(t, filepath.Join(outside, "secret.png"), pngBytes)
	mustWrite(t, filepath.Join(cwd, "notes.txt"), []byte("hello"))
	mustWrite(t, filepath.Join(cwd, "fake.png"), []byte("not an image at all"))
	if err := os.Symlink(filepath.Join(outside, "secret.png"), filepath.Join(cwd, "link.png")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cwd, "out")); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(cwd, "big.png")
	mustWrite(t, big, pngBytes)
	os.Truncate(big, maxWorkspaceImageBytes+1)

	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	for _, p := range []string{shot, ".monoagent/screenshots/a.png"} {
		if got, err := imageDataURLIn(cwd, p); err != nil || got != want {
			t.Errorf("%s: %.40q, %v", p, got, err)
		}
	}
	for name, p := range map[string]string{
		"dot-dot":          filepath.Join(cwd, "..", filepath.Base(outside), "secret.png"),
		"relative dot-dot": "../" + filepath.Base(outside) + "/secret.png",
		"symlinked file":   filepath.Join(cwd, "link.png"),
		"symlinked folder": "out/secret.png",
		"absolute outside": filepath.Join(outside, "secret.png"),
		"not an image":     filepath.Join(cwd, "notes.txt"),
		"image in name":    filepath.Join(cwd, "fake.png"),
		"too big":          big,
		"folder":           filepath.Join(cwd, ".monoagent"),
	} {
		if got, err := imageDataURLIn(cwd, p); err == nil {
			t.Errorf("%s (%s) was served: %.40q", name, p, got)
		}
	}
}

func showReply(conv ai.Conversation) fakeChatReply {
	b, _ := json.Marshal(conv.Record())
	return fakeChatReply{match: "chat history show", stdout: string(b)}
}

// ReadWorkspaceImage reads the coder conversation's folder from the CLI,
// once, and refuses a chat that has none.
func TestApp_ReadWorkspaceImage(t *testing.T) {
	cwd := t.TempDir()
	shot := mustWrite(t, filepath.Join(cwd, "s.png"), pngBytes)
	bin, argsLog := chatFakeCLI(t,
		showReply(ai.Conversation{ID: "coder-1", Mode: ai.ModeCoder, Cwd: cwd}),
	)
	a, _ := newCLIChatApp(t, bin)
	for i := 0; i < 2; i++ {
		if got, err := a.ReadWorkspaceImage("coder-1", shot); err != nil || !strings.HasPrefix(got, "data:image/png;base64,") {
			t.Fatalf("ReadWorkspaceImage = %.40q, %v", got, err)
		}
	}
	calls := readArgsLog(t, argsLog)
	if len(calls) != 1 || !strings.HasSuffix(calls[0], "chat history show -- coder-1") {
		t.Errorf("CLI calls = %q, want one `chat history show`", calls)
	}

	bin2, _ := chatFakeCLI(t, showReply(ai.Conversation{ID: "a-1", Mode: ai.ModeAssistant}))
	a2, _ := newCLIChatApp(t, bin2)
	if _, err := a2.ReadWorkspaceImage("a-1", shot); err == nil {
		t.Error("an assistant chat served an image")
	}
}

func TestSaveChatAttachmentWritesOneSpoolFile(t *testing.T) {
	spool := t.TempDir()
	now := time.Now()
	data := base64.StdEncoding.EncodeToString(pngBytes)
	p, err := saveChatAttachment(spool, "conv-1", "../../Screen Shot.png", data, now)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p) != "Screen_Shot.png" || !strings.HasPrefix(p, filepath.Join(spool, "conv-1")+"/") {
		t.Errorf("path = %s", p)
	}
	if b, _ := os.ReadFile(p); string(b) != string(pngBytes) {
		t.Errorf("content = %q", b)
	}
	// The same name again is a second file, not an overwrite.
	if p2, err := saveChatAttachment(spool, "conv-1", "Screen Shot.png", data, now); err != nil || p2 == p {
		t.Errorf("second save = %s, %v", p2, err)
	}

	for name, c := range map[string]struct{ conv, data string }{
		"conversation id with a separator": {"../x", data},
		"empty conversation id":            {"", data},
		"not base64":                       {"conv-1", "%%%"},
		"over 20 MB":                       {"conv-1", strings.Repeat("A", base64.StdEncoding.EncodedLen(maxChatAttachmentBytes+1))},
	} {
		if _, err := saveChatAttachment(spool, c.conv, "a.png", c.data, now); err == nil {
			t.Errorf("%s: saved", name)
		}
	}

	// A day later the old saves are gone.
	if _, err := saveChatAttachment(spool, "conv-2", "b.png", data, now.Add(chatSpoolTTL+time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("an attachment past its TTL is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(spool, "conv-1")); !os.IsNotExist(err) {
		t.Errorf("the emptied conversation folder is still there: %v", err)
	}
}

// StartChatTurn hands each attachment to the turn process as --attach,
// before the message.
func TestApp_StartChatTurn_PassesAttachments(t *testing.T) {
	bin, argsLog := chatFakeCLI(t, fakeChatReply{match: "chat --conversation", stdout: lines(
		admissionLine("coder-1", "turn-1", "active", false),
		eventLine("coder-1", "turn-1", 1, chatevents.EventTurnFinished, chatevents.TurnFinishedPayload{Status: chatevents.StatusCompleted, HistorySaved: true}),
	)})
	a, emitter := newCLIChatApp(t, bin)
	a.selectForTest("default")
	r := decodeStart(t, a.StartChatTurn("coder-1", "turn-1", "look", false, false, []string{"/s/a b.png", "/s/-x.txt"}))
	if !r.OK {
		t.Fatalf("Start = %+v", r)
	}
	waitForType(t, emitter, chatevents.EventTurnFinished, 5*time.Second)
	got := withoutCoderProbe(readArgsLog(t, argsLog))
	if len(got) == 0 || !strings.HasSuffix(got[0], "--instance "+a.chatSup.instanceID+" --attach=/s/a b.png --attach=/s/-x.txt -- look") {
		t.Errorf("argv = %q", got)
	}
}
