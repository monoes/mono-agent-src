package coderhost

import (
	"fmt"
	"sort"
	"strings"
)

// Control of descendants. Every operation takes the Caller of a verified session
// token and a target, and refuses unless the target is a descendant of the
// caller (IsDescendant, walked up in the database). The only thing a caller may
// do to itself is read its status.

func (s *Store) authorize(c Caller, target string) error {
	if c.ProfileID == "" || c.ConversationID == "" {
		return ErrSessionToken
	}
	if !s.IsDescendant(c.ProfileID, c.ConversationID, target) {
		return fmt.Errorf("%w: %s", ErrNotYours, target)
	}
	return nil
}

// SendToChild queues a message from a master to a descendant. It is not a
// person's send: the turn id starts with MasterTurnPrefix, so the command cannot
// ask for a mission and the host cannot find a confirmation for it.
func (s *Store) SendToChild(c Caller, target, message string) (Command, error) {
	if err := s.authorize(c, target); err != nil {
		return Command{}, err
	}
	if strings.TrimSpace(message) == "" {
		return Command{}, fmt.Errorf("%w: a send needs a message", ErrInvalid)
	}
	return s.Enqueue(Command{ProfileID: c.ProfileID, ConversationID: target, Kind: KindSend, TurnID: newMasterTurnID(), Message: message, Issuer: c.ConversationID})
}

// ControlChild queues a stop, pause or resume for a descendant.
func (s *Store) ControlChild(c Caller, target string, kind Kind) (Command, error) {
	switch kind {
	case KindStop, KindPause, KindResume:
	default:
		return Command{}, fmt.Errorf("%w: %q is not a control command", ErrInvalid, kind)
	}
	if err := s.authorize(c, target); err != nil {
		return Command{}, err
	}
	return s.Enqueue(Command{ProfileID: c.ProfileID, ConversationID: target, Kind: kind, Issuer: c.ConversationID})
}

// SetChild changes the agent-adjustable settings of a descendant. A patch that
// names any other setting is refused whole, before anything is stored.
func (s *Store) SetChild(c Caller, target string, patch []byte) (Settings, []string, error) {
	if err := s.authorize(c, target); err != nil {
		return Settings{}, nil, err
	}
	cur, err := s.GetSettings(c.ProfileID, target)
	if err != nil {
		return Settings{}, nil, err
	}
	next, keys, err := cur.Apply(patch)
	if err != nil {
		return Settings{}, nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if alias := next.MeshAlias; alias != "" && oneOf("mesh_alias", keys) {
		if taken, err := s.aliasTaken(c.ProfileID, target, alias); err != nil {
			return Settings{}, nil, err
		} else if taken {
			return Settings{}, nil, fmt.Errorf("%w: mesh_alias %q is already used by another session", ErrInvalid, alias)
		}
	}
	var banned []string
	for _, k := range keys {
		if !oneOf(k, AgentAdjustable) {
			banned = append(banned, k)
		}
	}
	if len(banned) > 0 {
		return Settings{}, nil, fmt.Errorf("%w: %s can only be changed by the operator", ErrOperatorOnly, strings.Join(banned, ", "))
	}
	if err := s.SaveSettings(c.ProfileID, target, next); err != nil {
		return Settings{}, nil, err
	}
	return next, keys, nil
}

// ChildStatus is what a master sees of a session of its own tree. It holds the
// host's records only, never text an agent wrote.
type ChildStatus struct {
	ConversationID string   `json:"conversationId"`
	Parent         string   `json:"parentConversationId,omitempty"`
	Role           string   `json:"role"`
	State          State    `json:"state"`
	Access         string   `json:"access"`
	Cwd            string   `json:"cwd"`
	TurnID         string   `json:"turnId,omitempty"`
	LastTurnID     string   `json:"lastTurnId,omitempty"`
	LastTurnStatus string   `json:"lastTurnStatus,omitempty"`
	Waiting        int      `json:"waitingCommands"`
	BudgetUSD      float64  `json:"budgetUsd"`
	CarvedUSD      float64  `json:"carvedUsd"`
	Settings       Settings `json:"settings"`
	UpdatedAt      string   `json:"updatedAt"`
}

// StatusOf is the status of the caller itself or of a descendant.
func (s *Store) StatusOf(c Caller, target string) (ChildStatus, error) {
	if target != c.ConversationID {
		if err := s.authorize(c, target); err != nil {
			return ChildStatus{}, err
		}
	} else if c.ProfileID == "" {
		return ChildStatus{}, ErrSessionToken
	}
	x, ok, err := s.GetSession(c.ProfileID, target)
	if err != nil {
		return ChildStatus{}, err
	}
	if !ok {
		return ChildStatus{}, fmt.Errorf("%w: %s", ErrNotYours, target)
	}
	return s.statusFrom(x)
}

func (s *Store) statusFrom(x Session) (ChildStatus, error) {
	st := ChildStatus{ConversationID: x.ConversationID, Parent: x.Parent, Role: x.Role, State: x.State, TurnID: x.TurnID, UpdatedAt: x.UpdatedAt,
		Waiting: s.PendingFor(x.ProfileID, x.ConversationID)}
	st.LastTurnID, st.LastTurnStatus = s.LastTurn(x.ProfileID, x.ConversationID)
	if cwd, access, ok, err := s.ConversationInfo(x.ProfileID, x.ConversationID); err != nil {
		return st, err
	} else if ok {
		st.Cwd, st.Access = cwd, access
	}
	var err error
	if st.Settings, err = s.GetSettings(x.ProfileID, x.ConversationID); err != nil {
		return st, err
	}
	if st.BudgetUSD, err = s.Budget(x.ProfileID, x.ConversationID); err != nil {
		return st, err
	}
	st.CarvedUSD, err = s.Carved(x.ProfileID, x.ConversationID)
	return st, err
}

// ListChildren returns every descendant of the caller, ordered by conversation id.
func (s *Store) ListChildren(c Caller) ([]ChildStatus, error) {
	if c.ProfileID == "" || c.ConversationID == "" {
		return nil, ErrSessionToken
	}
	all, err := s.ListSessions(c.ProfileID)
	if err != nil {
		return nil, err
	}
	out := []ChildStatus{}
	for _, x := range all {
		if !s.IsDescendant(c.ProfileID, c.ConversationID, x.ConversationID) {
			continue
		}
		st, err := s.statusFrom(x)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ConversationID < out[j].ConversationID })
	return out, nil
}

// aliasTaken says whether another session of the profile already has the mesh alias.
func (s *Store) aliasTaken(profileID, except, alias string) (bool, error) {
	all, err := s.ListSessions(profileID)
	if err != nil {
		return false, err
	}
	for _, x := range all {
		if x.ConversationID == except {
			continue
		}
		if st, err := s.GetSettings(profileID, x.ConversationID); err == nil && strings.EqualFold(st.MeshAlias, alias) {
			return true, nil
		}
	}
	return false, nil
}
