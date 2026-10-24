package chat

import (
	"strconv"

	"github.com/monoes/mono-agent/internal/agentbox"
	"github.com/monoes/mono-agent/internal/ai"
)

// The values of coder.box.
const (
	BoxOff    = "off"
	BoxCompat = "compat"
	BoxStrict = "strict"
)

// Codes of the typed errors a boxed coder turn can end in.
const (
	CodeBoxNeedsAPIMode = "box_needs_api_mode"
	CodeBoxOrgUnsupport = "box_org_unsupported"
	CodeBoxInvalid      = "box_setting_invalid"
)

type boxError struct{ code, msg string }

func (e *boxError) Error() string                   { return e.msg }
func (e *boxError) JSONErrorFields() map[string]any { return map[string]any{"code": e.code} }

// boxMode is coder.box as stored: absent is off, and any other value is returned as it is,
// so status shows a tampered one and coderBox refuses it.
func (s coderSettings) boxMode() string {
	if s.Box == "" {
		return BoxOff
	}
	return s.Box
}

// coderBox is the sandbox a coder lead turn runs in, or nil when coder.box is off (the
// default, which leaves the turn exactly as it was). Only coding access can run boxed:
// workspace and full access use monoagentcli with the user's database, which the box
// hides until the API client mode exists, and a dynamic org's workers would run outside
// the box the lead is in.
func coderBox(s coderSettings, access, cwd, runtime, orgMode string) (*agentbox.Box, error) {
	mode := s.boxMode()
	switch mode {
	case BoxOff:
		return nil, nil
	case BoxCompat, BoxStrict:
	default:
		return nil, &boxError{CodeBoxInvalid, "coder.box holds " + strconv.Quote(mode) + ", which is not off, compat or strict: " +
			"no turn runs until the operator sets it again (coder set --box)"}
	}
	if access == "" {
		access = ai.AccessFull // an empty access is full
	}
	if access != ai.AccessCoding {
		return nil, &boxError{CodeBoxNeedsAPIMode, "the sandbox (coder.box) is on, and a " + access + "-access turn needs monoagentcli with your database, " +
			"which the sandbox hides until the API client mode exists: use coding access or turn coder.box off"}
	}
	if orgMode == ai.OrgModeDynamic {
		return nil, &boxError{CodeBoxOrgUnsupport, "the sandbox (coder.box) is on, and a dynamic org's workers would run outside it: turn the org mode off or coder.box off"}
	}
	return &agentbox.Box{Mode: agentbox.Mode(mode), Cwd: cwd, Runtime: runtime}, nil
}
