package chat

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
)

// queueStartResult is `coder queue start --json`: what the app needs to run
// the first turn (StartChatTurn with prompt and attachments) and open the
// conversation's bubble.
type queueStartResult struct {
	ConversationID string   `json:"conversationId"`
	TurnID         string   `json:"turnId"`
	Prompt         string   `json:"prompt"`
	Attachments    []string `json:"attachments"`
	Cwd            string   `json:"cwd"`
	Runtime        string   `json:"runtime"`
	Model          string   `json:"model"`
	// Already: the item was started before; this is that start again (the
	// turn id is the same, so running it twice runs it once).
	Already bool `json:"already,omitempty"`
}

func newCoderQueueStartCmd(cfg *Deps) *cobra.Command {
	var cwd, runtimeID, model, effort string
	cmd := &cobra.Command{
		Use:   "start <id>",
		Short: "Create a queued chat's coder conversation and print its first turn (the app's Start)",
		Long: "Creates the coder conversation the way `chat history create --mode coder` does, in --cwd or the " +
			"folder the item was queued for, copies the item's folder into <folder>/" + queueContextDir + "/<id>/, " +
			"marks the item started, and prints {conversationId, turnId, prompt, attachments}. It never runs the " +
			"turn: the app does (`chat --conversation <id> --turn <turnId>`). Starting a started item prints the " +
			"same conversation and turn again. Refused inside an agent.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireUser(cfg, "start a queued coder chat"); err != nil {
				return err
			}
			res, err := startQueueItem(cmd, cfg, args[0], cwd, runtimeID, model, effort)
			if err != nil {
				return err
			}
			if cfg.JSONOutput() {
				return printQueueJSON(cmd, res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "conversation %s in %s\nrun it: monoagentcli chat --conversation %s --turn %s --prompt-stdin\n",
				res.ConversationID, res.Cwd, res.ConversationID, res.TurnID)
			return nil
		},
	}
	cmd.Flags().StringVar(&cwd, "cwd", "", "Folder to work in instead of the one the item was queued for")
	cmd.Flags().StringVar(&runtimeID, "runtime", "", "Agent runtime (default claude; see 'coder status')")
	cmd.Flags().StringVar(&model, "model", "", "Model for the runtime")
	cmd.Flags().StringVar(&effort, "effort", "", "Reasoning effort")
	return cmd
}

func startQueueItem(cmd *cobra.Command, cfg *Deps, id, cwd, runtimeID, model, effort string) (queueStartResult, error) {
	it, dir, err := readQueueItem(cfg, id)
	if err != nil {
		return queueStartResult{}, err
	}
	if it.Status == queueStatusStarted && it.ConversationID != "" {
		store, profileID, closeDB, err := openChatHistory(cfg)
		if err != nil {
			return queueStartResult{}, err
		}
		defer closeDB()
		conv, err := store.GetConversation(it.ConversationID, profileID)
		if err != nil {
			return queueStartResult{}, chatStoreErr(err)
		}
		res := queueResult(it, conv)
		res.Already = true
		return res, nil
	}
	if cwd == "" {
		cwd = it.Cwd
	}
	db, err := cfg.OpenDB()
	if err != nil {
		return queueStartResult{}, fmt.Errorf("initializing database: %w", err)
	}
	settings, err := loadCoderSettings(db.DB)
	db.Close()
	if err != nil {
		return queueStartResult{}, err
	}
	choice := coderFolderChoice{cwd: cwd}
	if root := expandHome(settings.WorkspaceRoot); cwd == "" || filepath.Clean(expandHome(cwd)) == filepath.Clean(root) {
		// The coder root may not exist yet: set it up as `--coder-root` does.
		choice = coderFolderChoice{root: true}
	}
	conv, err := createCoderConversation(cmd, cfg, ai.AccessFull, runtimeID, model, effort, "general", choice)
	if err != nil {
		return queueStartResult{}, err
	}
	if err := copyQueueContext(dir, filepath.Join(conv.Cwd, filepath.FromSlash(queueContextDir), it.ID)); err != nil {
		return queueStartResult{}, fmt.Errorf("copying the page context into %s: %w", conv.Cwd, err)
	}
	it.Status, it.ConversationID, it.TurnID = queueStatusStarted, conv.ID, uuid.NewString()
	it.StartedAt, it.Cwd = time.Now().UTC().Format(time.RFC3339), conv.Cwd
	if err := writeQueueItem(dir, it); err != nil {
		return queueStartResult{}, err
	}
	return queueResult(it, conv), nil
}

func queueResult(it queueItem, conv ai.Conversation) queueStartResult {
	ctxDir := filepath.Join(conv.Cwd, filepath.FromSlash(queueContextDir), it.ID)
	res := queueStartResult{ConversationID: conv.ID, TurnID: it.TurnID, Prompt: it.Prompt, Attachments: []string{},
		Cwd: conv.Cwd, Runtime: conv.RuntimeID, Model: conv.Model}
	for _, a := range it.Attachments {
		res.Attachments = append(res.Attachments, filepath.Join(ctxDir, filepath.FromSlash(a)))
	}
	return res
}

// copyQueueContext copies an item's folder (not item.json) to dst, and
// keeps the context folder out of git: it is page data, not the project's.
func copyQueueContext(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	ignore := filepath.Join(filepath.Dir(dst), ".gitignore")
	if _, err := os.Lstat(ignore); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(ignore, []byte("# Page context from queued coder chats (monoagentcli coder queue)\n*\n"), 0o644); err != nil {
			return err
		}
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "." {
			return nil
		}
		if rel == queueItemFile || strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil // a symlink or device never leaves the queue
		}
		return copyFile(p, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
