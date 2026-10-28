package chat

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	profilecli "github.com/monoes/mono-agent/internal/cli/profile"
	"github.com/monoes/mono-agent/internal/gitsafe"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/profiledir"
)

var (
	workspaceAdjectives = []string{"amber", "brisk", "calm", "clever", "crisp", "daring", "eager", "fuzzy", "gentle", "glad",
		"golden", "humble", "jolly", "keen", "lively", "lucky", "mellow", "nimble", "plucky", "quiet",
		"rapid", "rosy", "sunny", "swift", "tidy", "vivid", "witty", "zesty"}
	workspaceNouns = []string{"otter", "falcon", "maple", "comet", "badger", "heron", "cedar", "pebble", "lynx", "orchid",
		"harbor", "meadow", "walrus", "quartz", "sparrow", "willow", "panda", "ember", "fjord", "koala",
		"lagoon", "nebula", "raven", "tulip", "yak", "zephyr"}
)

func pick(words []string) string {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(words))))
	if err != nil {
		return words[0]
	}
	return words[n.Int64()]
}

// coderWorkspace is `coder workspace new --json`.
type coderWorkspace struct {
	Path    string                  `json:"path"`
	Created bool                    `json:"created"`
	Git     bool                    `json:"git"`
	Init    *monomind.WorkspaceInit `json:"init,omitempty"`
}

// initWorkspace sets a folder up for coder turns; a var so tests can skip
// the monomind subprocess.
var initWorkspace = monomind.InitWorkspace

// newCoderWorkspace creates a fresh, randomly named folder under root
// (<yyyymmdd>-<adjective>-<noun>), git-inits it so every change the agent
// makes can be reviewed and reverted, and initializes it as a monomind
// project.
func newCoderWorkspace(ctx context.Context, root, initTarget string) (*coderWorkspace, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("creating workspace root: %w", err)
	}
	day := time.Now().Format("20060102")
	var dir string
	for i := 0; ; i++ {
		name := fmt.Sprintf("%s-%s-%s", day, pick(workspaceAdjectives), pick(workspaceNouns))
		if i >= 20 {
			name = fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
		}
		dir = filepath.Join(root, name)
		err := os.Mkdir(dir, 0o755)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) || i > 25 {
			return nil, fmt.Errorf("creating workspace: %w", err)
		}
	}
	ws := &coderWorkspace{Path: dir, Created: true}
	if _, err := exec.LookPath("git"); err == nil {
		ws.Git = gitsafe.Command(ctx, "init", "-q", dir).Run() == nil
	}
	res, err := initWorkspace(ctx, "", dir, initTarget)
	if err != nil {
		return ws, fmt.Errorf("workspace %s created but not initialized: %w", dir, err)
	}
	ws.Init = res
	return ws, nil
}

// rootCoderWorkspace sets the coder root itself up as a working folder,
// shared by every chat that picks it: created if missing, git-initialized
// unless it already sits in a repository, and initialized as a monomind
// project with --if-missing (nothing already there is touched).
func rootCoderWorkspace(ctx context.Context, root, initTarget string) (*coderWorkspace, error) {
	created := false
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, fmt.Errorf("creating coder root: %w", err)
		}
		created = true
	}
	dir, err := resolveCoderCwd(root)
	if err != nil {
		return nil, err
	}
	ws := &coderWorkspace{Path: dir, Created: created}
	if _, err := exec.LookPath("git"); err == nil {
		if gitsafe.Command(ctx, "-C", dir, "rev-parse", "--is-inside-work-tree").Run() == nil {
			ws.Git = true
		} else {
			ws.Git = gitsafe.Command(ctx, "init", "-q", dir).Run() == nil
		}
	}
	res, err := initWorkspace(ctx, "", dir, initTarget)
	if err != nil {
		return ws, fmt.Errorf("coder root %s not initialized: %w", dir, err)
	}
	ws.Init = res
	return ws, nil
}

// coderDefaultWorkspace is `coder workspace default --json`: the active
// profile's own folder, ready for a coder chat.
type coderDefaultWorkspace struct {
	Path    string `json:"path"`
	Profile struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"profile"`
	// Source is "profile" for the profile's default folder and
	// "profile_custom_folder" when the profile was pointed elsewhere.
	Source  string                  `json:"source"`
	Created bool                    `json:"created"`
	Init    *monomind.WorkspaceInit `json:"init,omitempty"`
}

// profileCoderWorkspace sets the profile's folder itself up as the coder
// working folder: created if missing, then the runtime's add-only init
// (nothing already there is touched). It is deliberately not git-initialized:
// the folder holds the profile's vault and knowledge graph, and may be the
// user's own project.
func profileCoderWorkspace(ctx context.Context, db *sql.DB, profileID, initTarget string) (*coderDefaultWorkspace, error) {
	p, err := lookupProfile(db, profileID)
	if err != nil {
		return nil, err
	}
	root := profiledir.Root(db, p.ID)
	if root == "" {
		return nil, clicore.InvalidInput("profile %q has no usable folder", p.ID)
	}
	created := false
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, fmt.Errorf("creating profile folder: %w", err)
		}
		created = true
	}
	dir, err := resolveCoderCwd(root)
	if err != nil {
		return nil, err
	}
	ws := &coderDefaultWorkspace{Path: dir, Source: "profile", Created: created}
	ws.Profile.ID, ws.Profile.Name = p.ID, p.Name
	if !profiledir.IsDefaultManaged(db, p.ID) {
		ws.Source = "profile_custom_folder"
	}
	res, err := initWorkspace(ctx, "", dir, initTarget)
	if err != nil {
		return ws, fmt.Errorf("profile folder %s not initialized: %w", dir, err)
	}
	ws.Init = res
	return ws, nil
}

// resolveCoderCwd validates a user-picked folder: any existing directory,
// made absolute with symlinks resolved, so resuming always finds the same
// Claude Code session folder.
func resolveCoderCwd(p string) (string, error) {
	abs, err := filepath.Abs(expandHome(p))
	if err != nil {
		return "", clicore.InvalidInput("--cwd: %v", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", clicore.InvalidInput("--cwd %s: %v", p, err)
	}
	fi, err := os.Stat(real)
	if err != nil || !fi.IsDir() {
		return "", clicore.InvalidInput("--cwd %s is not a folder", p)
	}
	return real, nil
}

func newCoderWorkspaceCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "workspace", Short: "Coder mode working folders"}

	var root, newRuntime, rootRuntime, defaultRuntime string
	newCmd := &cobra.Command{
		Use:   "new",
		Short: "Create a fresh, randomly named test folder and initialize it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, err := cfg.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			s, _, rt, err := requireCoderReady(cmd, db.DB, newRuntime)
			if err != nil {
				return err
			}
			if root == "" {
				root = s.WorkspaceRoot
			}
			ws, err := newCoderWorkspace(cmd.Context(), expandHome(root), rt.InitTarget)
			if err != nil {
				return err
			}
			if cfg.JSONOutput() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(ws)
			}
			fmt.Fprintln(cmd.OutOrStdout(), ws.Path)
			return nil
		},
	}
	newCmd.Flags().StringVar(&root, "root", "", "Create it here instead of the configured workspace root")
	newCmd.Flags().StringVar(&newRuntime, "runtime", monomind.DefaultCoderRuntime, "Runtime whose setup files the folder gets")
	clicore.WithJSONErrors(cfg.JSONOutput, newCmd)

	var limit int
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List folders coder conversations have used, most recent first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, profileID, closeStore, err := openChatHistory(cfg)
			if err != nil {
				return err
			}
			defer closeStore()
			list, err := store.ListCoderWorkspaces(profileID, limit)
			if err != nil {
				return err
			}
			type item struct {
				Path          string `json:"path"`
				LastUsed      string `json:"lastUsed"`
				Conversations int    `json:"conversations"`
				Exists        bool   `json:"exists"`
			}
			out := make([]item, 0, len(list))
			for _, w := range list {
				fi, err := os.Stat(w.Path)
				out = append(out, item{w.Path, w.LastUsed, w.Conversations, err == nil && fi.IsDir()})
			}
			if cfg.JSONOutput() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
			}
			for _, w := range out {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%d\n", w.Path, w.LastUsed, w.Conversations)
			}
			return nil
		},
	}
	listCmd.Flags().IntVar(&limit, "limit", 20, "Maximum folders to list")
	clicore.WithJSONErrors(cfg.JSONOutput, listCmd)

	rootCmd := &cobra.Command{
		Use:   "root",
		Short: "Set up the coder root itself as a working folder and print its path",
		Long: "Chats that pick the coder root work directly in it, sharing the folder. This creates it " +
			"if missing, git-initializes it unless it is already in a repository, and adds any missing " +
			"monomind setup files for the runtime (--runtime). Nothing already there is changed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, err := cfg.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			s, _, rt, err := requireCoderReady(cmd, db.DB, rootRuntime)
			if err != nil {
				return err
			}
			ws, err := rootCoderWorkspace(cmd.Context(), expandHome(s.WorkspaceRoot), rt.InitTarget)
			if err != nil {
				return err
			}
			if cfg.JSONOutput() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(ws)
			}
			fmt.Fprintln(cmd.OutOrStdout(), ws.Path)
			return nil
		},
	}
	rootCmd.Flags().StringVar(&rootRuntime, "runtime", monomind.DefaultCoderRuntime, "Runtime whose setup files the folder gets")
	clicore.WithJSONErrors(cfg.JSONOutput, rootCmd)

	defaultCmd := &cobra.Command{
		Use:   "default",
		Short: "Set up the folder of the profile named by --profile as the coder working folder and print it",
		Long: "A coder chat works in the folder of the profile named by --profile unless the user picks another. This " +
			"creates it if missing and adds any missing monomind setup files for the runtime (--runtime). " +
			"Nothing already there is changed. Prints {path, profile{id,name}, source} with --json.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, err := cfg.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			_, _, rt, err := requireCoderReady(cmd, db.DB, defaultRuntime)
			if err != nil {
				return err
			}
			ws, err := profileCoderWorkspace(cmd.Context(), db.DB, cfg.ProfileID(), rt.InitTarget)
			if err != nil {
				return err
			}
			if cfg.JSONOutput() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(ws)
			}
			fmt.Fprintln(cmd.OutOrStdout(), ws.Path)
			return nil
		},
	}
	defaultCmd.Flags().StringVar(&defaultRuntime, "runtime", monomind.DefaultCoderRuntime, "Runtime whose setup files the folder gets")
	clicore.WithJSONErrors(cfg.JSONOutput, defaultCmd)

	cmd.AddCommand(newCmd, rootCmd, listCmd, defaultCmd)
	return cmd
}

// lookupProfile resolves nameOrID to a profile row, as `profile` does.
func lookupProfile(db *sql.DB, nameOrID string) (*profilecli.Profile, error) {
	return profilecli.Deps{NotFound: clicore.NotFound, InvalidInput: clicore.InvalidInput}.LookupProfile(db, nameOrID)
}
