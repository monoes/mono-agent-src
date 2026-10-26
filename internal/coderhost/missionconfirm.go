package coderhost

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/secrets"
)

// A mission confirmation is the operator's explicit "yes, run this send as a
// mission". It is a record the operator-gated `coder sessions send --mission`
// writes (in the configs table, under a reserved prefix no other writer may
// use: internal/operator.ReservedConfig); the command row, the message, the
// attachments and the mesh block carry nothing of it.
//
// The record is bound to one queued COMMAND (not just a turn id, which a caller
// can choose and reuse), and it carries a MAC under a key derived from the
// profile's vault data key (secrets.DeriveKey), so a row written without that
// key, by the config commands or straight into the table, does not verify. The
// host looks the record up only for a command the person marked as a mission and
// hands the chat child a token; the child verifies the MAC and consumes the
// record, so one confirmation starts one turn, once. The record lives as long as
// its command can wait (MaxSendAge) and is revoked when the command is
// cancelled, expires or fails.

// MissionConfirmTTL is how long a confirmation lives: as long as the command it
// belongs to may wait for the host. Revocation on every way a command ends
// unstarted, not a short life, is what keeps a stale record from being used.
const MissionConfirmTTL = MaxSendAge

// MissionConfirmGrace is how long the host waits for the record of a command
// marked as a mission: the command is queued before its record is written.
const MissionConfirmGrace = time.Minute

// MissionTokenEnv is the variable the host hands a chat child its confirmation
// token in (see MissionConfirm.Token).
const MissionTokenEnv = "MONOAGENT_MISSION_TOKEN"

var (
	stashMu      sync.Mutex
	stashedToken string
)

// StashMissionToken moves the host's confirmation token out of the Go
// environment into memory. The binary calls it before anything else can start a
// subprocess (the account guard, the update and tool probes), so no child of this
// process inherits it; the chat command then takes it with TakeMissionToken.
// Limit: os.Unsetenv clears Go's copy, not the environment block the kernel gave
// the process at exec, so the token stays readable from that block
// (/proc/<pid>/environ on Linux, `ps eww` on macOS) by a process of the same user
// until the child exits. What bounds that is that the token is single use: it is
// dead once ConsumeConfirm has run, early in the turn.
func StashMissionToken() {
	stashMu.Lock()
	defer stashMu.Unlock()
	if v := os.Getenv(MissionTokenEnv); v != "" {
		stashedToken = v
	}
	_ = os.Unsetenv(MissionTokenEnv)
}

// TakeMissionToken returns the stashed token (stashing it first if it is still in
// the environment) and forgets it, so a token is handed out once. This is about
// what this process's own children inherit; see StashMissionToken for what the
// process environment still shows.
func TakeMissionToken() string {
	StashMissionToken()
	stashMu.Lock()
	defer stashMu.Unlock()
	t := stashedToken
	stashedToken = ""
	return t
}

const confirmPrefix = "coder-mission-confirm/"

// wantsMissionMark is stored in the error column of a pending send the person
// asked to run as a mission. It is NOT authority: it only tells the host that a
// missing or expired record is a failure to report, not a plain turn to run. The
// host clears it when the command ends.
const wantsMissionMark = "wants-mission"

const confirmPurpose = "coder-mission-confirm"

// MissionConfirm is what the operator confirmed: this turn of this conversation,
// carried by this command, for the mission state it was given in (Session is the
// open mission session of the conversation then, 0 when there was none and the
// lead was only proposing).
type MissionConfirm struct {
	ProfileID      string `json:"profile"`
	ConversationID string `json:"conversation"`
	TurnID         string `json:"turn"`
	Command        int64  `json:"command"`
	CommandCreated string `json:"commandCreated"` // the command row's created_at, as stored: binds the record to that row, not to an id a later row could reuse
	Session        int64  `json:"session"`
	Nonce          string `json:"nonce"`
	Expires        string `json:"expires"`
	Mac            string `json:"mac"`
}

// Token is what the host hands the chat child: the command and the nonce.
func (c MissionConfirm) Token() string { return strconv.FormatInt(c.Command, 10) + "." + c.Nonce }

func parseToken(token string) (cmd int64, nonce string, ok bool) {
	a, b, found := strings.Cut(token, ".")
	id, err := strconv.ParseInt(a, 10, 64)
	return id, b, found && err == nil && id > 0 && b != ""
}

// HostMade says whether a turn id is one the host makes on its own (a
// check-in), not a person's send. Such a turn can never be confirmed.
func HostMade(turnID string) bool { return strings.HasPrefix(turnID, "checkin-") }

var errHostMade = errors.New("coder host: a host-started turn cannot carry a mission confirmation")

func confirmName(profile, conv, turn string, cmd int64) string {
	return confirmPrefix + profile + "/" + conv + "/" + turn + "/" + strconv.FormatInt(cmd, 10)
}

func (c MissionConfirm) name() string {
	return confirmName(c.ProfileID, c.ConversationID, c.TurnID, c.Command)
}

// mac authenticates every field of the record but the MAC itself.
func (c MissionConfirm) mac(key []byte) string {
	m := hmac.New(sha256.New, key)
	for _, f := range []string{c.ProfileID, c.ConversationID, c.TurnID, strconv.FormatInt(c.Command, 10), strconv.FormatInt(c.Session, 10), c.Nonce, c.Expires, c.CommandCreated} {
		fmt.Fprintf(m, "%d:%s;", len(f), f) // length-prefixed: no two field splits give the same input
	}
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Store) confirmKey(profileID string) ([]byte, error) {
	return secrets.DeriveKey(context.Background(), s.db, profileID, confirmPurpose)
}

// Confirm records the operator's confirmation of one queued send command. Only
// the operator-gated send command calls it, after Enqueue: the command must be
// this profile's pending send, for this conversation and turn, marked as a
// mission.
func (s *Store) Confirm(cmd Command, session int64) (MissionConfirm, error) {
	if HostMade(cmd.TurnID) {
		return MissionConfirm{}, errHostMade
	}
	got, err := s.pendingMissionSend(cmd)
	if err != nil {
		return MissionConfirm{}, err
	}
	key, err := s.confirmKey(cmd.ProfileID)
	if err != nil {
		return MissionConfirm{}, fmt.Errorf("coder host: the confirmation could not be signed: %w", err)
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return MissionConfirm{}, err
	}
	c := MissionConfirm{ProfileID: cmd.ProfileID, ConversationID: cmd.ConversationID, TurnID: cmd.TurnID, Command: cmd.ID, Session: session, CommandCreated: got.CreatedAt,
		Nonce: hex.EncodeToString(nonce), Expires: s.now().Add(MissionConfirmTTL).UTC().Format(timeLayout)}
	c.Mac = c.mac(key)
	b, _ := json.Marshal(c)
	// Expired confirmations of any command are swept as a new one is written.
	s.sweepConfirms()
	confirmBeforeInsert()
	if _, err = s.db.Exec(`INSERT INTO configs (name, config_data, created_at, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(name) DO UPDATE SET config_data = excluded.config_data, updated_at = excluded.updated_at`, c.name(), string(b)); err != nil {
		return c, err
	}
	confirmAfterInsert()
	// Every way a command ends unstarted (host.end, CancelPendingSends, ExpireSends,
	// SweepOrphans) first moves its status off pending and only then revokes. So a
	// command that is still pending here is ended after the record is in place, and that
	// end's revoke finds the record; one that is no longer pending had its end begin
	// before this check, and the record goes here. What is not guaranteed: a record is
	// never read as valid for a command that is not pending (PeekConfirm checks), and a
	// record of a command that ended is also dropped by the next sweep.
	if _, err := s.pendingMissionSend(cmd); err != nil {
		s.dropConfirm(c.name())
		return MissionConfirm{}, err
	}
	return c, nil
}

// confirmBeforeInsert runs between Confirm's check and its insert, and
// confirmAfterInsert between its insert and its recheck. A test sets them to end
// the command in those windows.
var confirmBeforeInsert = func() {}
var confirmAfterInsert = func() {}

// pendingMissionSend returns the stored command when it is this profile's
// pending send, for this conversation and turn, marked as a mission.
func (s *Store) pendingMissionSend(cmd Command) (Command, error) {
	got, ok, err := s.GetCommand(cmd.ID)
	if err != nil {
		return Command{}, err
	}
	if !ok || got.Kind != KindSend || got.Status != CmdPending || !got.WantsMission || got.ProfileID != cmd.ProfileID ||
		got.ConversationID != cmd.ConversationID || got.TurnID != cmd.TurnID || cmd.TurnID == "" {
		return Command{}, errors.New("coder host: a confirmation belongs to a pending send queued as a mission")
	}
	return got, nil
}

func (c MissionConfirm) live(now time.Time) bool {
	t, err := time.Parse(timeLayout, c.Expires)
	return err == nil && now.Before(t)
}

func (s *Store) sweepConfirms() {
	rows, err := s.db.Query(`SELECT name, config_data FROM configs WHERE name LIKE ?`, confirmPrefix+"%")
	if err != nil {
		return
	}
	var dead []string
	type liveRec struct {
		name string
		cmd  int64
	}
	var live []liveRec
	for rows.Next() {
		var name, raw string
		var c MissionConfirm
		if rows.Scan(&name, &raw) != nil {
			continue
		}
		if json.Unmarshal([]byte(raw), &c) != nil || !c.live(s.now()) {
			dead = append(dead, name)
		} else {
			live = append(live, liveRec{name, c.Command})
		}
	}
	rows.Close()
	// A record whose command is gone (deleted) or ended unstarted is dead too,
	// whatever its expiry says. A command that is done keeps its record: the chat
	// child admits itself (and the host marks the command done) before it consumes
	// the record, so a starting child must not be cut off.
	for _, r := range live {
		var status string
		if e := s.db.QueryRow(`SELECT status FROM coder_session_commands WHERE id = ?`, r.cmd).Scan(&status); e == sql.ErrNoRows || (e == nil && status != CmdPending && status != CmdDone) {
			dead = append(dead, r.name)
		}
	}
	for _, n := range dead {
		s.dropConfirm(n)
	}
}

func (s *Store) dropConfirm(name string) {
	_, _ = s.db.Exec(`DELE`+`TE FROM configs WHERE name = ?`, name)
}

// Revoke drops the unconsumed confirmation of a command that will not start a
// turn (it was cancelled, expired or failed, or could not be recorded).
func (s *Store) Revoke(profileID, conversationID, turnID string, commandID int64) {
	s.dropConfirm(confirmName(profileID, conversationID, turnID, commandID))
}

// readConfirm returns the record of a command when it is genuine: it parses,
// names exactly this command, turn, conversation and profile, carries a MAC under
// the profile's key and has not expired.
func (s *Store) readConfirm(profileID, conversationID, turnID string, commandID int64) (MissionConfirm, string, bool) {
	var raw string
	if err := s.db.QueryRow(`SELECT config_data FROM configs WHERE name = ?`, confirmName(profileID, conversationID, turnID, commandID)).Scan(&raw); err != nil {
		return MissionConfirm{}, "", false // no record (or unreadable): no confirmation
	}
	var c MissionConfirm
	if json.Unmarshal([]byte(raw), &c) != nil || c.ProfileID != profileID || c.ConversationID != conversationID || c.TurnID != turnID ||
		c.Command != commandID || c.Nonce == "" || !c.live(s.now()) {
		return MissionConfirm{}, "", false
	}
	key, err := s.confirmKey(profileID)
	if err != nil || !hmac.Equal([]byte(c.mac(key)), []byte(c.Mac)) {
		return MissionConfirm{}, "", false
	}
	// The record belongs to the command row it was written for: the same id on a
	// row created at another time is a different command.
	var created string
	if s.db.QueryRow(`SELECT created_at FROM coder_session_commands WHERE id = ?`, commandID).Scan(&created) != nil || created != c.CommandCreated {
		return MissionConfirm{}, "", false
	}
	return c, raw, true
}

// PeekConfirm is what the host looks at when it starts a send: the genuine,
// live confirmation of this command. A command not marked as a mission, and a
// host-made turn, never has one.
func (s *Store) PeekConfirm(c Command) (MissionConfirm, bool) {
	if HostMade(c.TurnID) || !c.WantsMission {
		return MissionConfirm{}, false
	}
	// Only a command that is still pending starts a turn, whatever the record says.
	var status string
	if s.db.QueryRow(`SELECT status FROM coder_session_commands WHERE id = ?`, c.ID).Scan(&status) != nil || status != CmdPending {
		return MissionConfirm{}, false
	}
	mc, _, ok := s.readConfirm(c.ProfileID, c.ConversationID, c.TurnID, c.ID)
	return mc, ok
}

// ConsumeConfirm is the chat child's check. It does not look at the command's
// status: the child consumes after it has admitted itself, and by then the host
// may already have marked the command done. The check: the token must be the one of a
// genuine record (MAC and nonce) for this turn, in the mission state (session)
// the operator confirmed, and the record is deleted in the same statement that
// proves it still holds, so a second start, a restart or a concurrent child gets
// false.
func (s *Store) ConsumeConfirm(profileID, conversationID, turnID, token string, session int64) bool {
	if HostMade(turnID) {
		return false
	}
	cmd, nonce, ok := parseToken(token)
	if !ok {
		return false
	}
	c, raw, ok := s.readConfirm(profileID, conversationID, turnID, cmd)
	if !ok || !hmac.Equal([]byte(c.Nonce), []byte(nonce)) || c.Session != session {
		return false
	}
	res, err := s.db.Exec(`DELE`+`TE FROM configs WHERE name = ? AND config_data = ?`, c.name(), raw)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// readMark turns the stored marker of a pending mission send into WantsMission.
func (c *Command) readMark() {
	if c.Status == CmdPending && c.Error == wantsMissionMark {
		c.WantsMission, c.Error = true, ""
	}
}

// pendingMissionIDs lists the pending sends marked as missions that match where
// (their records are revoked once the sends are failed).
func (s *Store) pendingMissionIDs(where string, args ...any) []Command {
	rows, err := s.db.Query(`SELECT id, profile_id, conversation_id, turn_id FROM coder_session_commands WHERE kind = 'send' AND status = 'pending' AND error = ? AND `+where,
		append([]any{wantsMissionMark}, args...)...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Command
	for rows.Next() {
		var c Command
		if rows.Scan(&c.ID, &c.ProfileID, &c.ConversationID, &c.TurnID) == nil {
			out = append(out, c)
		}
	}
	return out
}

// revokeCommands is called after the commands' status was moved off pending. The
// ids were read before that update, so a command queued and confirmed in between
// is not among them; the sweep drops its record too.
func (s *Store) revokeCommands(cmds []Command) {
	for _, c := range cmds {
		s.Revoke(c.ProfileID, c.ConversationID, c.TurnID, c.ID)
	}
	s.sweepConfirms()
}
