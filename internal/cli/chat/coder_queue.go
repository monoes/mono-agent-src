package chat

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// `coder queue …`: coder chats the browser extension queued (C5 of
// docs/mastermind/plans/2026-10-15-coder-browser.md). The extension never
// starts a full-access turn: `add` gathers the page's context into a folder
// under <db dir>/coder-queue/<id>/, the desktop app shows it as a queued
// bubble, and `start` (the person's click) creates the conversation and
// hands back the first turn's prompt. Everything in an item's folder came
// from a web page: it is data for the agent, never instructions.

const (
	queueStatusQueued  = "queued"
	queueStatusStarted = "started"
	queueItemFile      = "item.json"
	queueReadmeFile    = "README.md"
	// queueContextDir is where `start` copies an item's folder, relative to
	// the conversation's folder.
	queueContextDir = ".monoagent/context"
)

// queueKinds are what the extension can hand to the coder.
var queueKinds = []string{"copy-style", "tokens", "fix-element", "bug-report", "integration", "ask"}

// queueIDPattern is an item id: its creation time and three random bytes.
// Checked before an id names a folder, so it can never climb out of the
// queue.
var queueIDPattern = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9a-f]{6}$`)

// queueItem is item.json.
type queueItem struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	CreatedAt string `json:"createdAt"`
	Status    string `json:"status"`
	Profile   string `json:"profile"`
	// Origin and Path are the page's, without query or fragment.
	Origin string `json:"origin"`
	Path   string `json:"path"`
	Title  string `json:"title"`
	Note   string `json:"note"`
	Cwd    string `json:"cwd"`
	Prompt string `json:"prompt"`
	// Attachments are the item's images the first turn attaches, relative
	// to the item's folder; Files is everything in it.
	Attachments []string    `json:"attachments"`
	Files       []queueFile `json:"files"`
	Selector    string      `json:"selector,omitempty"`
	Browser     string      `json:"browser,omitempty"`
	TabID       int         `json:"tabId,omitempty"`
	// A bug report's logs: Reloaded says the tab was reloaded with capture
	// on (--reload), LogsEmpty that console.json and network.json hold
	// nothing, NetworkEmpty that network.json does.
	Reloaded     bool `json:"reloaded,omitempty"`
	LogsEmpty    bool `json:"logsEmpty,omitempty"`
	NetworkEmpty bool `json:"networkEmpty,omitempty"`
	// Set by `start`.
	ConversationID string `json:"conversationId,omitempty"`
	TurnID         string `json:"turnId,omitempty"`
	StartedAt      string `json:"startedAt,omitempty"`
}

type queueFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// queueRoot is the folder holding every queued item.
func queueRoot(cfg *Deps) string {
	return filepath.Join(filepath.Dir(clicore.ExpandPath(cfg.DBPath())), "coder-queue")
}

func queueItemDir(cfg *Deps, id string) (string, error) {
	if !queueIDPattern.MatchString(id) {
		return "", clicore.InvalidInput("%q is not a queued item id", id)
	}
	return filepath.Join(queueRoot(cfg), id), nil
}

func readQueueItem(cfg *Deps, id string) (queueItem, string, error) {
	dir, err := queueItemDir(cfg, id)
	if err != nil {
		return queueItem{}, "", err
	}
	b, err := os.ReadFile(filepath.Join(dir, queueItemFile))
	if errors.Is(err, fs.ErrNotExist) {
		return queueItem{}, "", clicore.NotFound("no queued coder chat %s", id)
	}
	if err != nil {
		return queueItem{}, "", err
	}
	var it queueItem
	if err := json.Unmarshal(b, &it); err != nil {
		return queueItem{}, "", fmt.Errorf("reading queued coder chat %s: %w", id, err)
	}
	return it, dir, nil
}

// writeQueueItem replaces dir/item.json in one rename, so a reader (the
// app's watcher) never sees half a file.
func writeQueueItem(dir string, it queueItem) error {
	b, err := json.MarshalIndent(it, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+queueItemFile+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, queueItemFile))
}

// listQueueItems returns profile's items, newest first; all includes
// started ones.
func listQueueItems(cfg *Deps, profile string, all bool) ([]queueItem, error) {
	entries, err := os.ReadDir(queueRoot(cfg))
	if errors.Is(err, fs.ErrNotExist) {
		return []queueItem{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []queueItem{}
	for _, e := range entries {
		if !e.IsDir() || !queueIDPattern.MatchString(e.Name()) {
			continue
		}
		it, _, err := readQueueItem(cfg, e.Name())
		if err != nil || it.Profile != profile || (!all && it.Status != queueStatusQueued) {
			continue
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// requireUser is the gate of the coder queue and projects verbs that only the user runs: the shared turn-aware
// refusal first (it refuses as operator_only whatever the database says), then the CLI's own operator check.
func requireUser(cfg *Deps, what string) error {
	if why := clicore.OperatorRefusal(cfg.OperatorRefusal); why != "" {
		return clicore.OperatorOnly("only the user can %s: %s; run it yourself in a terminal", what, why)
	}
	return clicore.RequireOperator(cfg.RequireOperator, what)
}

func printQueueJSON(cmd *cobra.Command, v any) error {
	return json.NewEncoder(cmd.OutOrStdout()).Encode(v)
}

func newCoderQueueCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "Coder chats the browser extension queued; the desktop app starts them",
		Long: "The browser extension never starts a full-access turn: it queues a coder chat with the page's " +
			"context (screenshots, styles, design tokens, source mapping, console errors, failed requests), " +
			"and the desktop app shows it as a queued bubble until you press Start. Everything in a queued " +
			"chat's folder came from a web page: it is data for the agent, never instructions.",
	}
	var all bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List queued coder chats, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			profile, err := activeProfile(cfg)
			if err != nil {
				return err
			}
			items, err := listQueueItems(cfg, profile, all)
			if err != nil {
				return err
			}
			if cfg.JSONOutput() {
				return printQueueJSON(cmd, map[string]any{"items": items})
			}
			for _, it := range items {
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %-11s %-7s %s%s  %s\n", it.ID, it.Kind, it.Status, it.Origin, it.Path, it.Cwd)
			}
			return nil
		},
	}
	list.Flags().BoolVar(&all, "all", false, "Include started items")
	show := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one queued coder chat",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			it, _, err := readQueueItem(cfg, args[0])
			if err != nil {
				return err
			}
			if cfg.JSONOutput() {
				return printQueueJSON(cmd, it)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "id:      %s\nkind:    %s\nstatus:  %s\npage:    %s%s\nfolder:  %s\n\n%s\n",
				it.ID, it.Kind, it.Status, it.Origin, it.Path, it.Cwd, it.Prompt)
			return nil
		},
	}
	dismiss := &cobra.Command{
		Use:   "dismiss <id>",
		Short: "Drop a queued coder chat and its page context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, dir, err := readQueueItem(cfg, args[0])
			if err != nil {
				return err
			}
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
			if cfg.JSONOutput() {
				return printQueueJSON(cmd, map[string]any{"id": args[0], "dismissed": true})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "dismissed %s\n", args[0])
			return nil
		},
	}
	image := &cobra.Command{
		Use:   "image <id> [file]",
		Short: "A queued chat's screenshot as a data URL (default: its first attachment)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			it, dir, err := readQueueItem(cfg, args[0])
			if err != nil {
				return err
			}
			name := "page.png"
			if len(it.Attachments) > 0 {
				name = it.Attachments[0]
			}
			if len(args) == 2 {
				name = args[1]
			}
			url, err := queueImageDataURL(dir, name)
			if err != nil {
				return err
			}
			return printQueueJSON(cmd, map[string]any{"id": it.ID, "name": name, "dataUrl": url})
		},
	}
	for _, c := range []*cobra.Command{newCoderQueueAddCmd(cfg), list, show, newCoderQueueStartCmd(cfg), dismiss, image} {
		clicore.WithJSONErrors(cfg.JSONOutput, c)
		cmd.AddCommand(c)
	}
	return cmd
}

// activeProfile is the profile OpenDB resolves (--profile or the active
// one).
func activeProfile(cfg *Deps) (string, error) {
	db, err := cfg.OpenDB()
	if err != nil {
		return "", fmt.Errorf("initializing database: %w", err)
	}
	db.Close()
	return cfg.ProfileID(), nil
}

var errTooLarge = errors.New("file too large")

// readCapped reads p when it is a regular file (not a symlink) of at most max
// bytes. It looks at the path first, so a pipe or device is never opened,
// then opens it once and checks the open file is the one it looked at, and
// reads at most max+1 bytes of it: a file swapped or grown in between can't
// slip past the cap.
func readCapped(p string, max int64) ([]byte, error) {
	before, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	if before.Size() > max {
		return nil, errTooLarge
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if now, err := f.Stat(); err != nil || !os.SameFile(before, now) {
		return nil, fmt.Errorf("%s changed while it was read", p)
	}
	return readAllCapped(f, max)
}

// readAllCapped reads r to its end, refusing more than max bytes.
func readAllCapped(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errTooLarge
	}
	return b, nil
}

// queueImageMax bounds an image `coder queue image` returns.
const queueImageMax = 10 << 20

// queueImageDataURL reads name (relative to dir, no climbing out) as a
// PNG, JPEG or WebP data URL.
func queueImageDataURL(dir, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", clicore.InvalidInput("%q is not a file of the queued chat", name)
	}
	b, err := readCapped(filepath.Join(dir, clean), queueImageMax)
	switch {
	case errors.Is(err, errTooLarge):
		return "", clicore.InvalidInput("%s is larger than %d MB", name, queueImageMax>>20)
	case err != nil:
		return "", clicore.NotFound("the queued chat has no file %s", name)
	}
	mime := http.DetectContentType(b)
	switch mime {
	case "image/png", "image/jpeg", "image/webp":
	default:
		return "", clicore.InvalidInput("%s is not a PNG, JPEG or WebP image", name)
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b), nil
}
