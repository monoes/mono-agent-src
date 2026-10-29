package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	goruntime "runtime"
)

// Clickable file paths in chat messages. A model writes those messages, so a
// token is untrusted input: it only ever reaches the file manager (reveal) or
// a text read of a small markdown/text file inside the chat's folder. Nothing
// here executes a file or hands one to its default handler.

// maxChatPreviewBytes bounds ReadChatMarkdown.
const maxChatPreviewBytes = 512 << 10

const maxChatTokenLen = 4096

var chatPreviewExts = map[string]bool{".md": true, ".markdown": true, ".txt": true}

// ChatPathInfo is what ResolveChatPath knows about a token.
type ChatPathInfo struct {
	Kind        string `json:"kind"` // "file", "dir" or "missing"
	Previewable bool   `json:"previewable"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
}

var chatLineSuffix = regexp.MustCompile(`^(.+?):(\d+)(?::\d+)?$`)

// parseChatToken strips a trailing :line[:col] and refuses tokens that are
// empty, oversized, contain control characters, or start with '-' (an option
// to the opener).
func parseChatToken(token string) (string, int, error) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > maxChatTokenLen {
		return "", 0, errors.New("not a usable path")
	}
	if strings.HasPrefix(token, "-") {
		return "", 0, errors.New("a path may not start with '-'")
	}
	for _, r := range token {
		if r < 0x20 || r == 0x7f {
			return "", 0, errors.New("path contains a control character")
		}
	}
	line := 0
	if m := chatLineSuffix.FindStringSubmatch(token); m != nil {
		token = m[1]
		line, _ = strconv.Atoi(m[2])
	}
	return token, line, nil
}

// resolveChatToken turns a token into a cleaned, symlink-resolved absolute
// path that exists. Relative tokens are taken from cwd (which must itself be
// absolute); "~" and "~/..." expand to the user's home.
func resolveChatToken(cwd, token string) (string, int, error) {
	p, line, err := parseChatToken(token)
	if err != nil {
		return "", 0, err
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, herr := os.UserHomeDir()
		if herr != nil || home == "" {
			return "", 0, errors.New("no home folder")
		}
		p = filepath.Join(home, p[1:])
	}
	if !filepath.IsAbs(p) {
		if cwd == "" || !filepath.IsAbs(cwd) {
			return "", 0, errors.New("a relative path needs the chat's folder")
		}
		p = filepath.Join(cwd, p)
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		return "", 0, err
	}
	if strings.HasPrefix(real, "-") {
		return "", 0, errors.New("a path may not start with '-'")
	}
	return real, line, nil
}

// insideDir reports whether p (already symlink-resolved) is root or below it.
func insideDir(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// confinedChatPath is resolveChatToken plus the requirement that the result
// lies inside cwd after both sides had their symlinks resolved.
func confinedChatPath(cwd, token string) (string, int, error) {
	if cwd == "" {
		return "", 0, errors.New("this chat has no folder to read from")
	}
	root, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", 0, err
	}
	p, line, err := resolveChatToken(root, token)
	if err != nil {
		return "", 0, err
	}
	if !insideDir(root, p) {
		return "", 0, fmt.Errorf("%s is outside the chat's folder", token)
	}
	return p, line, nil
}

func chatPreviewable(p string, fi os.FileInfo) bool {
	return fi.Mode().IsRegular() && chatPreviewExts[strings.ToLower(filepath.Ext(p))] && fi.Size() <= maxChatPreviewBytes
}

func resolveChatPathInfo(cwd, token string) ChatPathInfo {
	p, line, err := resolveChatToken(cwd, token)
	if err != nil {
		return ChatPathInfo{Kind: "missing"}
	}
	fi, err := os.Stat(p)
	if err != nil {
		return ChatPathInfo{Kind: "missing"}
	}
	switch {
	case fi.IsDir():
		return ChatPathInfo{Kind: "dir", Path: p, Line: line}
	case fi.Mode().IsRegular():
		info := ChatPathInfo{Kind: "file", Path: p, Line: line}
		if _, _, perr := confinedChatPath(cwd, token); perr == nil {
			info.Previewable = chatPreviewable(p, fi)
		}
		return info
	}
	return ChatPathInfo{Kind: "missing"} // fifo, device, socket: nothing to offer
}

// readChatMarkdown returns the text of a .md/.markdown/.txt regular file
// inside cwd, at most maxChatPreviewBytes.
func readChatMarkdown(cwd, token string) (string, error) {
	p, _, err := confinedChatPath(cwd, token)
	if err != nil {
		return "", err
	}
	if !chatPreviewExts[strings.ToLower(filepath.Ext(p))] {
		return "", errors.New("only .md, .markdown and .txt files can be previewed")
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxChatPreviewBytes+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxChatPreviewBytes {
		return "", errors.New("file is too large to preview (over 512 KiB)")
	}
	if !utf8.Valid(b) || strings.IndexByte(string(b), 0) >= 0 {
		return "", errors.New("not a text file")
	}
	return string(b), nil
}

// chatOpener is the OS side of reveal, injectable so tests launch nothing.
type chatOpener struct {
	goos     string
	lookPath func(string) (string, error)
	run      func(name string, args ...string) error // waits for the result
	start    func(name string, args ...string) error // fire and forget
}

var defaultChatOpener = chatOpener{
	goos:     goruntime.GOOS,
	lookPath: exec.LookPath,
	run: func(name string, args ...string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, name, args...)
		suppressConsole(c)
		return c.Run()
	},
	start: func(name string, args ...string) error {
		c := exec.Command(name, args...)
		suppressConsole(c)
		if err := c.Start(); err != nil {
			return err
		}
		go func() { _ = c.Wait() }()
		return nil
	},
}

// reveal shows p (an existing, resolved file or folder) in the file manager.
// Arguments go as argv, never through a shell.
func (o chatOpener) reveal(p string, isDir bool) error {
	if !filepath.IsAbs(p) || strings.HasPrefix(p, "-") {
		return errors.New("refusing a non-absolute path")
	}
	switch o.goos {
	case "darwin":
		if isDir {
			return o.start("open", p)
		}
		return o.start("open", "-R", p)
	case "windows":
		if isDir {
			return o.start("explorer", p)
		}
		return o.start("explorer", "/select,"+p)
	default:
		if isDir {
			return o.start("xdg-open", p)
		}
		if _, err := o.lookPath("dbus-send"); err == nil {
			uri := (&url.URL{Scheme: "file", Path: p}).String()
			uri = strings.ReplaceAll(uri, ",", "%2C") // dbus-send splits array items on commas
			err := o.run("dbus-send", "--session", "--print-reply=literal",
				"--dest=org.freedesktop.FileManager1", "--type=method_call",
				"/org/freedesktop/FileManager1", "org.freedesktop.FileManager1.ShowItems",
				"array:string:"+uri, "string:")
			if err == nil {
				return nil
			}
		}
		return o.start("xdg-open", filepath.Dir(p))
	}
}

func revealChatPath(o chatOpener, cwd, token string) error {
	p, _, err := resolveChatToken(cwd, token)
	if err != nil {
		return err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	if !fi.IsDir() && !fi.Mode().IsRegular() {
		return errors.New("not a file or folder")
	}
	return o.reveal(p, fi.IsDir())
}

// chatCwd is a coder conversation's folder, "" when there is none.
func (a *App) chatCwd(conversationID string) string {
	if conversationID == "" || a.chatSup == nil {
		return ""
	}
	pid, err := a.profileID()
	if err != nil {
		return ""
	}
	cwd, err := a.chatSup.conversationCwd(pid, conversationID)
	if err != nil {
		return ""
	}
	return cwd
}

// ResolveChatPath says whether a path a chat message names exists, and
// whether it can be previewed. Relative paths are taken from the
// conversation's folder.
func (a *App) ResolveChatPath(conversationID, token string) ChatPathInfo {
	if a.signedOut() != nil {
		return ChatPathInfo{Kind: "missing"}
	}
	return resolveChatPathInfo(a.chatCwd(conversationID), token)
}

// RevealChatPath shows an existing file or folder in the file manager.
func (a *App) RevealChatPath(conversationID, token string) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	return revealChatPath(defaultChatOpener, a.chatCwd(conversationID), token)
}

// ReadChatMarkdown returns a small markdown/text file of the conversation's
// folder for the in-app preview.
func (a *App) ReadChatMarkdown(conversationID, token string) (string, error) {
	if err := a.signedOut(); err != nil {
		return "", err
	}
	return readChatMarkdown(a.chatCwd(conversationID), token)
}
