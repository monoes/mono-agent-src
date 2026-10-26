package mcp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/monoes/mono-agent/internal/tasks"
)

// maxClientLabel is the room an actor name leaves for the client's name: the
// task store takes names of 64 characters, and "agent:", "#" and four hex
// digits take 11 of them.
const maxClientLabel = 53

// newActorSuffix makes the four hex digits that tell two sessions of one
// client apart (spec D18). A variable, so a test can give servers known ones.
var newActorSuffix = func() string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// clientLabel is a client's name as it may stand in an actor name: ASCII
// letters, digits, '.', '_' and '@' as they are, and every other run of
// characters one '-' ('#' and ':' are the separators of the name), at most
// maxClientLabel long; "mcp" when nothing is left.
func clientLabel(name string) string {
	var b strings.Builder
	gap := false
	for _, r := range name {
		if b.Len() > maxClientLabel {
			break
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '@' {
			if gap && b.Len() > 0 {
				b.WriteByte('-')
			}
			gap = false
			b.WriteRune(r)
			continue
		}
		gap = true
	}
	label := b.String()
	if len(label) > maxClientLabel {
		label = label[:maxClientLabel]
	}
	if label = strings.TrimRight(label, "-"); label == "" {
		return "mcp"
	}
	return label
}

// recordClient keeps the name a client gives in initialize's clientInfo; the
// first initialize that names a client wins. Params without one, or that do not
// parse, change nothing.
func (s *Server) recordClient(params json.RawMessage) {
	var p struct {
		ClientInfo struct {
			Name string `json:"name"`
		} `json:"clientInfo"`
	}
	if len(params) == 0 || json.Unmarshal(params, &p) != nil || strings.TrimSpace(p.ClientInfo.Name) == "" {
		return
	}
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	if s.clientName == "" {
		s.clientName = p.ClientInfo.Name
	}
}

// taskActor is who the task tools act as (spec D18): an agent named
// agent:<client>#<four hex digits>, after the client that sent initialize and a
// suffix of this server's own, so two sessions of one client are two claimants.
// The model does not choose it. The first task call fixes it, so a later
// initialize cannot rename the holder of a claim.
func (s *Server) taskActor() tasks.Actor {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	if s.actorName == "" {
		s.actorName = "agent:" + clientLabel(s.clientName) + "#" + s.actorSuffix
	}
	return tasks.Actor{Kind: tasks.Agent, Name: s.actorName, Session: boundSession(), Task: boundTask(), Verifier: boundVerifier()}
}
