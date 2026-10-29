package chat

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

// pngHeader is enough of a PNG for content sniffing.
var pngHeader = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func writeFile(t *testing.T, path string, data []byte) string {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAttachmentSourcesRefusesWhatItCannotCopy(t *testing.T) {
	dir := t.TempDir()
	ok := writeFile(t, filepath.Join(dir, "ok.txt"), []byte("x"))
	big := filepath.Join(dir, "big.bin")
	f, _ := os.Create(big)
	f.Truncate(maxAttachmentBytes + 1)
	f.Close()
	many := make([]string, maxAttachments+1)
	for i := range many {
		many[i] = ok
	}
	for name, paths := range map[string][]string{
		"folder":   {dir},
		"too big":  {big},
		"missing":  {filepath.Join(dir, "nope.png")},
		"too many": many,
	} {
		if _, err := attachmentSources(paths); exitCodeFor(err) != 3 {
			t.Errorf("%s: err = %v, want an exit-3 refusal", name, err)
		}
	}
	got, err := attachmentSources([]string{ok})
	if err != nil || len(got) != 1 || got[0] != ok {
		t.Errorf("attachmentSources(ok) = %v, %v", got, err)
	}
}

func TestCopyAttachmentsSafeDistinctNames(t *testing.T) {
	src, cwd := t.TempDir(), t.TempDir()
	sub := filepath.Join(src, "sub")
	os.Mkdir(sub, 0o755)
	srcs := []string{
		writeFile(t, filepath.Join(src, "Screen Shot 1.png"), pngHeader),
		writeFile(t, filepath.Join(sub, "Screen Shot 1.png"), pngHeader),
		writeFile(t, filepath.Join(src, "..hidden;rm -rf.txt"), []byte("hello")),
		writeFile(t, filepath.Join(src, "скрин.webp"), []byte("RIFF....WEBPVP8 ")),
		writeFile(t, filepath.Join(src, "noext"), pngHeader),
	}
	// A .gitignore of the user's own is left alone.
	os.MkdirAll(filepath.Join(cwd, ".monoagent"), 0o755)
	writeFile(t, filepath.Join(cwd, ".monoagent", ".gitignore"), []byte("mine\n"))

	atts, err := copyAttachments(cwd, "turn-1", srcs)
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"Screen_Shot_1.png", "Screen_Shot_1-2.png", "hidden_rm_-rf.txt", "скрин.webp", "noext"}
	wantMimes := []string{"image/png", "image/png", "text/plain", "image/webp", "image/png"}
	dir := filepath.Join(cwd, ".monoagent", "attachments", "turn-1")
	for i, a := range atts {
		if a.Name != wantNames[i] || a.Mime != wantMimes[i] || a.Path != filepath.Join(dir, wantNames[i]) {
			t.Errorf("attachment %d = %+v, want name %q mime %q in %s", i, a, wantNames[i], wantMimes[i], dir)
		}
		if b, err := os.ReadFile(a.Path); err != nil || int64(len(b)) != a.Size {
			t.Errorf("copy %s: %d bytes, size %d, %v", a.Path, len(b), a.Size, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(cwd, ".monoagent", ".gitignore")); string(b) != "mine\n" {
		t.Errorf(".gitignore = %q, want the user's own kept", b)
	}

	// A fresh folder gets one that keeps the copies out of git.
	cwd2 := t.TempDir()
	if _, err := copyAttachments(cwd2, "t2", srcs[:1]); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(cwd2, ".monoagent", ".gitignore")); string(b) != "*\n" {
		t.Errorf("new .gitignore = %q", b)
	}
	// A folder that is gone is not recreated.
	gone := filepath.Join(t.TempDir(), "gone")
	if _, err := copyAttachments(gone, "t3", srcs[:1]); exitCodeFor(err) != 3 {
		t.Errorf("missing folder: %v", err)
	}
	if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("copyAttachments recreated %s", gone)
	}
}

func TestAttachmentPromptBlock(t *testing.T) {
	if attachmentPrompt(nil) != "" {
		t.Error("a turn without attachments got a block")
	}
	got := attachmentPrompt([]chatevents.TurnAttachment{
		{Name: "a.png", Path: "/w/.monoagent/attachments/t/a.png", Mime: "image/png", Size: 3 << 10},
		{Name: "b.log", Path: "/w/.monoagent/attachments/t/b.log", Mime: "text/plain", Size: 5 << 20},
	})
	for _, want := range []string{
		"\n\nAttached files (",
		"- /w/.monoagent/attachments/t/a.png (image/png, 3 KB)\n",
		"- /w/.monoagent/attachments/t/b.log (text/plain, 5.0 MB)\n",
		"file-reading tool",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("block lacks %q:\n%s", want, got)
		}
	}
}

// promptCapturingTranscript is a coder agent exec that saves its prompt
// file to promptLog and finishes.
func promptCapturingTranscript(promptLog string) string {
	return `  pf=""; prev=""
  for a in "$@"; do if [ "$prev" = "--prompt-file" ]; then pf="$a"; fi; prev="$a"; done
  cp "$pf" '` + promptLog + `'
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"ok"}'
  echo '{"v":1,"type":"done","exit_code":0}'`
}

// A coder turn's attachments are copied into its folder, listed in the
// prompt and in turn.started, and passed as --attach only when monomind
// takes it.
func TestCoderTurnWithAttachments(t *testing.T) {
	for _, native := range []bool{false, true} {
		dbPath := newChatCLITestDB(t)
		promptLog := filepath.Join(t.TempDir(), "prompt")
		bin, argsLog := writeCoderMonomind(t, promptCapturingTranscript(promptLog))
		caps := slices.Clone(monomind.CoderCapabilities)
		if native {
			caps = append(caps, monomind.CapAgentExecAttachments)
		}
		withCoderCaps(t, caps...)
		setCoderSettings(t, dbPath, coderSettings{Enabled: true})
		cwd := t.TempDir()
		conv, err := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, cwd)
		if err != nil {
			t.Fatal(err)
		}
		shot := writeFile(t, filepath.Join(t.TempDir(), "shot.png"), pngHeader)

		out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "turn-1", "--attach", shot, "--", "what is wrong here?")
		if err != nil {
			t.Fatalf("native=%v: %v\n%s", native, err, out)
		}
		copied := filepath.Join(cwd, ".monoagent", "attachments", "turn-1", "shot.png")
		if _, err := os.Stat(copied); err != nil {
			t.Fatalf("native=%v: no copy: %v", native, err)
		}
		prompt, _ := os.ReadFile(promptLog)
		if !strings.HasPrefix(string(prompt), "what is wrong here?\n\nAttached files") || !strings.Contains(string(prompt), "- "+copied+" (image/png") {
			t.Errorf("native=%v: prompt = %q", native, prompt)
		}
		execLine := lastAgentExecLine(t, argsLog)
		if got := strings.Contains(execLine, "--attach "+copied); got != native {
			t.Errorf("native=%v: --attach in argv = %v: %s", native, got, execLine)
		}

		j := parseJournaledTurn(t, out)
		var started chatevents.TurnStartedPayload
		json.Unmarshal(j.byType(chatevents.EventTurnStarted)[0].Payload, &started)
		if started.Text != "what is wrong here?" || len(started.Attachments) != 1 || withoutIdentity(started.Attachments[0]) != (chatevents.TurnAttachment{Name: "shot.png", Path: copied, Mime: "image/png", Size: int64(len(pngHeader))}) {
			t.Errorf("native=%v: turn.started = %+v", native, started)
		}
	}
}

// --attach is refused, before any turn exists, outside coder chats and for
// a turn id that cannot name a folder.
func TestAttachRefusals(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	bin, argsLog := writeCoderMonomind(t, promptCapturingTranscript(filepath.Join(t.TempDir(), "p")))
	withCoderCaps(t, monomind.CoderCapabilities...)
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	store := openTestChatStore(t, dbPath)
	assistant, _ := store.CreateConversation("default", "agent", "general", "claude", "", "")
	coder, _ := store.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	file := writeFile(t, filepath.Join(t.TempDir(), "a.txt"), []byte("a"))

	for _, c := range []struct {
		name, turn string
		args       []string
	}{
		{"assistant conversation", "t-a", []string{"--conversation", assistant.ID, "--turn", "t-a"}},
		{"unsafe turn id", "../t", []string{"--conversation", coder.ID, "--turn", "../t"}},
		{"plain chat", "", []string{"--runtime", "claude"}},
	} {
		args := append(append(c.args, "--attach", file, "--"), "hi")
		if out, err := runChatCmd(t, dbPath, bin, args...); exitCodeFor(err) != 3 {
			t.Errorf("%s: err = %v, want exit 3\n%s", c.name, err, out)
		}
		if c.turn != "" {
			if _, err := store.GetTurn(c.turn, "default"); !errors.Is(err, ai.ErrTurnNotFound) {
				t.Errorf("%s: the refused turn was admitted (%v)", c.name, err)
			}
		}
	}
	if logged, _ := os.ReadFile(argsLog); strings.Contains(string(logged), "agent exec") {
		t.Error("a refused --attach still ran the agent")
	}
}

// A plain `chat --mode coder` turn copies its attachments into its folder too.
func TestPlainCoderTurnWithAttachment(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	promptLog := filepath.Join(t.TempDir(), "prompt")
	bin, _ := writeCoderMonomind(t, promptCapturingTranscript(promptLog))
	withCoderCaps(t, monomind.CoderCapabilities...)
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	cwd := t.TempDir()
	file := writeFile(t, filepath.Join(t.TempDir(), "notes.md"), []byte("# notes"))

	if out, err := runChatCmd(t, dbPath, bin, "--mode", "coder", "--cwd", cwd, "--attach", file, "--", "read my notes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	matches, _ := filepath.Glob(filepath.Join(cwd, ".monoagent", "attachments", "*", "notes.md"))
	if len(matches) != 1 {
		t.Fatalf("copies = %v", matches)
	}
	if prompt, _ := os.ReadFile(promptLog); !strings.Contains(string(prompt), "- "+matches[0]+" (") {
		t.Errorf("prompt = %q", prompt)
	}
}

// withoutIdentity is att without the device and inode the host records for the copy (they differ per run).
func withoutIdentity(att chatevents.TurnAttachment) chatevents.TurnAttachment {
	att.Dev, att.Ino = 0, 0
	return att
}
