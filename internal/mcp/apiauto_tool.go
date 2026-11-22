package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/openaiapi"
)

// apiAutoTools is the tool over the `auto` model of the OpenAI-compatible API, which is a Jev surface
// of the profile (internal/jev/jevconf) and not a setting of the server: what `monoagentcli jev
// enable|disable api_auto` does, for the call's profile.
func apiAutoTools() []tool {
	return []tool{{
		name:        "api_auto_set",
		description: "Switch the auto model of the OpenAI-compatible API (the Jev surface api_auto) on or off for the call's profile, as `monoagentcli jev enable|disable api_auto`. Switching on sends request text to the api_auto surface's decision provider (TypeSafe by default), so it needs the operator's --allow-api-exposure and acknowledge_egress: true. " + apiToolsDetails,
		schema: objSchema(map[string]interface{}{
			"enabled":            boolParam("true switches the auto model on for the call's profile, false switches it off"),
			"acknowledge_egress": boolParam("Needed with enabled true, on a server whose operator started it with --allow-api-exposure: says that the user knows what is sent to the api_auto surface's decision provider (TypeSafe by default) while it is on (the refusal lists it). Not needed to switch off"),
		}, "enabled"),
		annotations: map[string]bool{"readOnlyHint": false},
		mutating:    true,
		handler:     toolAPIAutoSet,
	}}
}

func toolAPIAutoSet(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		Enabled           *bool `json:"enabled"`
		AcknowledgeEgress bool  `json:"acknowledge_egress"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	if a.Enabled == nil {
		return nil, errors.New("enabled is required: true switches the auto model on, false switches it off")
	}
	surface := jevconf.APIAuto
	// What leaves the machine is the operator's decision, made when the server starts (the flag of the
	// exposure of the API's server), and not one that a model makes by setting an argument; then the
	// caller's acknowledgement makes the data visible before it moves. Switching off sends nothing.
	if *a.Enabled && !s.opts.AllowAPIExposure {
		return nil, egressNotAllowed(surface)
	}
	if *a.Enabled && !a.AcknowledgeEgress {
		return nil, egressRefusal(surface)
	}
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	if err := jevconf.SetEnabled(rt.db.DB, profileOf(ctx), surface, *a.Enabled); err != nil {
		return nil, fmt.Errorf("switch the auto model: %w", err)
	}

	// The document of `jev enable|disable --json`.
	doc := map[string]interface{}{
		"profile_id": profileOf(ctx),
		"surface":    string(surface),
		"enabled":    *a.Enabled,
	}
	if *a.Enabled {
		doc["threshold"] = jevconf.Threshold(rt.db.DB, profileOf(ctx), surface, jevconf.DefaultThreshold[surface])
		doc["egress"] = jevconf.Egress[surface]
	}
	// And what `api status --json` says of the auto model now, which the command says on stderr when
	// there is no key. The key is only asked for, never read: this never decrypts anything.
	st := openaiapi.DefaultAuto(rt.db.DB).Status(ctx, profileOf(ctx))
	doc["auto"] = openaiapi.AutoReport{Available: st.Available, Missing: st.Missing, KeySource: st.KeySource}
	return doc, nil
}

// egressNotAllowed is the refusal to switch a surface on on a server whose operator did not allow it:
// what would leave the machine, why no argument can allow it, and what the user can do.
func egressNotAllowed(surface jevconf.Surface) error {
	var b strings.Builder
	b.WriteString("Switching the auto model on sends data to the api_auto surface's Jev decision provider (TypeSafe by default) for every request that asks for the model auto:")
	for _, e := range jevconf.Egress[surface] {
		b.WriteString("\n- " + e)
	}
	b.WriteString("\nNothing was changed. This MCP server was not started with --allow-api-exposure (or MONOAGENT_MCP_ALLOW_API_EXPOSURE=1), ")
	b.WriteString("and no argument of this tool can allow it, because the model sets the arguments and only the operator sets that flag. ")
	b.WriteString("The user can switch it on with `monoagentcli jev enable api_auto` or in the desktop app (Settings › Jev). Switching it off needs neither the flag nor an acknowledgement.")
	return errors.New(b.String())
}

// egressRefusal is the refusal to switch a surface on without the acknowledgement: what would leave
// the machine, in the words `jev enable` prints, and how to go on.
func egressRefusal(surface jevconf.Surface) error {
	var b strings.Builder
	b.WriteString("Switching the auto model on sends data to the api_auto surface's Jev decision provider (TypeSafe by default) for every request that asks for the model auto:")
	for _, e := range jevconf.Egress[surface] {
		b.WriteString("\n- " + e)
	}
	b.WriteString("\nPass acknowledge_egress: true to switch it on once the user has agreed to that. Nothing was changed. Switching it off needs no acknowledgement.")
	return errors.New(b.String())
}
