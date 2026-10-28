package chat

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// `coder sessions settings`: the per-session knobs shown in the coder pane
// (check-ins, autonomy, limits, mesh) and whether the session may run
// unattended. Reading is open; changing is the operator's.

type sessionSettingsView struct {
	ConversationID string              `json:"conversationId"`
	Role           string              `json:"role"`
	Unattended     bool                `json:"unattended"`
	Access         string              `json:"access"`
	Settings       coderhost.Settings  `json:"settings"`
	OperatorOnly   []string            `json:"operatorOnly"`
	Allowed        map[string][]string `json:"allowed"`
	Changed        []string            `json:"changed,omitempty"`
	// Checkin is set for a master: how many check-in turns ran in a row with no message
	// from a person between them, against its limit. Waiting says the scheduler would check
	// in (it is unattended and has children) but has reached the limit, so it waits until
	// someone writes to the master.
	Checkin *checkinView `json:"checkin,omitempty"`
}

type checkinView struct {
	Streak  int  `json:"streak"`
	Limit   int  `json:"limit"`
	Waiting bool `json:"waiting"`
}

func newCoderSessionsSettingsCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "Show or change a coder session's settings",
	}
	cmd.AddCommand(newCoderSessionsSettingsGetCmd(cfg), newCoderSessionsSettingsSetCmd(cfg))
	return cmd
}

// sessionSettingsOf reads a coder conversation's settings and its unattended flag.
func sessionSettingsOf(cfg *Deps, conversationID string, changed []string) (sessionSettingsView, error) {
	store, profileID, closeDB, err := openChatHistory(cfg)
	if err != nil {
		return sessionSettingsView{}, err
	}
	defer closeDB()
	conv, err := store.GetConversation(conversationID, profileID)
	if err != nil {
		return sessionSettingsView{}, chatStoreErr(err)
	}
	if conv.Scoped() {
		return sessionSettingsView{}, clicore.InvalidInput("conversation %s is not a coder conversation", conversationID)
	}
	return sessionSettingsFrom(cfg, profileID, conv, changed)
}

func sessionSettingsFrom(cfg *Deps, profileID string, conv ai.Conversation, changed []string) (sessionSettingsView, error) {
	db, err := cfg.OpenDB()
	if err != nil {
		return sessionSettingsView{}, err
	}
	defer db.Close()
	cs := coderhost.NewStore(db.DB)
	settings, err := cs.GetSettings(profileID, conv.ID)
	if err != nil {
		return sessionSettingsView{}, err
	}
	v := sessionSettingsView{
		ConversationID: conv.ID, Role: "solo", Unattended: true, Access: conv.EffectiveAccess(),
		Settings: settings, OperatorOnly: coderhost.OperatorOnly, Changed: changed,
		Allowed: map[string][]string{
			"autonomy_preset": coderhost.AutonomyPresets, "gap_classes": coderhost.GapClasses,
			"checkin_triggers": coderhost.CheckinEvents, "mesh_accept": coderhost.MeshAccepts,
		},
	}
	if x, ok, err := cs.GetSession(profileID, conv.ID); err != nil {
		return v, err
	} else if ok {
		v.Role, v.Unattended = x.Role, x.Unattended
	}
	if v.Role == "master" {
		if streak, _, _, err := cs.CheckinStreak(profileID, conv.ID); err == nil {
			// Waiting is what the scheduler would do: no check-in for a master that is not unattended or has
			// no running child, and none past the limit.
			children := 0
			if all, err := cs.ListSessions(profileID); err == nil {
				for _, x := range all {
					if x.Parent == conv.ID && x.State != coderhost.StateStopped {
						children++
					}
				}
			}
			v.Checkin = &checkinView{Streak: streak, Limit: settings.CheckinLimit, Waiting: v.Unattended && children > 0 && streak >= settings.CheckinLimit}
		}
	}
	return v, nil
}

func newCoderSessionsSettingsGetCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get CONVERSATION",
		Short: "Show a coder session's settings and whether it runs unattended",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			visible, verr := visibleToToken(cfg)
			if verr != nil {
				return verr
			}
			if visible != nil && !visible(args[0]) { // the same refusal whether the session exists or not
				return masterErr(coderhost.ErrNotYours)
			}
			v, err := sessionSettingsOf(cfg, args[0], nil)
			if err != nil {
				return err
			}
			return printQueueJSON(cmd, v)
		},
	}
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

func newCoderSessionsSettingsSetCmd(cfg *Deps) *cobra.Command {
	var patch, unattended, role, parent string
	var budget float64
	cmd := &cobra.Command{
		Use:   "set CONVERSATION",
		Short: "Change settings with a JSON object naming only the settings to change",
		Long: "--patch takes a JSON object with the settings to change (see `settings get` for their names); a setting " +
			"it does not name keeps its value, and a name that is not a setting is refused. --unattended true|false " +
			"false turns off the host's own scheduled check-ins for the session; a message you send runs either way. --role makes it a master of child sessions (which the " +
			"host checks in on at the master's interval) or a child of one.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if sessionTokenFromEnv() != "" { // a master's turn: agent-adjustable settings of its descendants only
				return masterSet(cfg, cmd, args[0], patch, unattended != "" || role != "" || parent != "" || cmd.Flags().Changed("budget-usd"))
			}
			if err := clicore.RequireOperator(cfg.RequireOperator, "change a coder session's settings"); err != nil {
				return err
			}
			if patch == "" && unattended == "" && role == "" && !cmd.Flags().Changed("budget-usd") {
				return clicore.InvalidInput("nothing to change: give --patch, --unattended, --role and/or --budget-usd")
			}
			if parent != "" && role != "child" {
				return clicore.InvalidInput("--parent goes with --role child")
			}
			if unattended != "" && unattended != "true" && unattended != "false" {
				return clicore.InvalidInput("--unattended is true or false")
			}
			profileID, err := coderConversation(cfg, args[0])
			if err != nil {
				return err
			}
			db, err := cfg.OpenDB()
			if err != nil {
				return err
			}
			cs := coderhost.NewStore(db.DB)
			var changed []string
			if patch != "" {
				cur, err := cs.GetSettings(profileID, args[0])
				if err != nil {
					db.Close()
					return err
				}
				next, keys, err := cur.Apply([]byte(patch))
				if err != nil {
					db.Close()
					return clicore.InvalidInput("%v", err)
				}
				if err := cs.SaveSettings(profileID, args[0], next); err != nil {
					db.Close()
					return err
				}
				changed = keys
			}
			if role != "" {
				if parent != "" {
					if _, err := coderConversation(cfg, parent); err != nil {
						db.Close()
						return err
					}
				}
				if err := cs.SetRole(profileID, args[0], role, parent); err != nil {
					db.Close()
					return clicore.InvalidInput("%v", err)
				}
				changed = append(changed, "role")
			}
			if cmd.Flags().Changed("budget-usd") {
				if err := cs.SetBudget(profileID, args[0], budget); err != nil {
					db.Close()
					return clicore.InvalidInput("%v", err)
				}
				changed = append(changed, "budget_usd")
			}
			if unattended != "" {
				if err := cs.SetUnattended(profileID, args[0], unattended == "true"); err != nil {
					db.Close()
					return err
				}
				changed = append(changed, "unattended")
			}
			db.Close()
			v, err := sessionSettingsOf(cfg, args[0], changed)
			if err != nil {
				return err
			}
			return printQueueJSON(cmd, v)
		},
	}
	cmd.Flags().StringVar(&patch, "patch", "", "JSON object with the settings to change")
	cmd.Flags().StringVar(&unattended, "unattended", "", "true or false: false turns off the host's own scheduled check-ins for the session")
	cmd.Flags().StringVar(&role, "role", "", "solo, master or child (a child needs --parent: a master with room for it)")
	cmd.Flags().Float64Var(&budget, "budget-usd", 0, "The session's spend cap in USD that its children are carved from (0 = none; not below what its children hold)")
	cmd.Flags().StringVar(&parent, "parent", "", "the master conversation, with --role child")
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}
