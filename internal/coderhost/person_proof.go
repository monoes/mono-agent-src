package coderhost

import (
	"errors"
	"strings"

	"github.com/monoes/mono-agent/internal/tasks"
)

// The person's own answers (the reply to a plan, to a parked question, to a critical ask) are queued with turn ids that
// start with answer-p- or ask-p-. The pursuit loop's away limits reset on exactly these (pursuit_person.go), so the
// prefixes are reserved: enqueueOn refuses them unless the command was built by one of the person-gated paths of this
// package, which set Command.person (unexported: no other package can). The exported helpers that build such a command
// (TellLeadAboutPlan, DenyPlan, AnswerAncestorQuestion) also take a PersonProof, which only a plain operator actor
// can obtain (ProvePerson); the callers of all four are pinned by TestEveryPersonQueueingSiteIsKnown, which scans
// internal/ and cmd/.

// PersonProof says the caller went through the person gate and holds the operator's own actor. The zero value proves nothing.
type PersonProof struct{ ok bool }

// ProvePerson accepts the actor a person gate returned. It refuses an agent, a capture, and an operator actor bound to a
// session, task, verifier or chat, or marked as the mission planner: none of them is the person at a terminal.
func ProvePerson(a tasks.Actor) (PersonProof, error) {
	if a.Kind != tasks.Human || a.Session != 0 || a.Task != 0 || a.Verifier || a.Conversation != "" || a.MissionPlanner {
		return PersonProof{}, errors.New("coder host: only the person's own actor can send the person's words")
	}
	return PersonProof{ok: true}, nil
}

func (p PersonProof) check() error {
	if !p.ok {
		return errors.New("coder host: this is the person's to do; no proof of the person was given")
	}
	return nil
}

// reservedPersonTurn says whether a turn id is one of the person's own answers' prefixes.
func reservedPersonTurn(turnID string) bool {
	return strings.HasPrefix(turnID, AnswerPrefix+answerFromPerson+"-") || strings.HasPrefix(turnID, AskPrefix+askFromPerson+"-")
}
