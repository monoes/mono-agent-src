package a2ui

import (
	"fmt"
	"sort"
	"strings"
)

// MaxMessagesPerCall is how many envelopes one ui_render call takes
// (dynorg.MaxUIMessages; a test in internal/a2ui/eval keeps them equal).
const MaxMessagesPerCall = 20

// PromptSnippet is the instruction block appended to the system prompt of
// every agent that has the ui_render tool (the lead and the workers of a
// dynamic-org coder chat; see internal/dynorg/ui_tool.go for the sites). The
// component list is derived from the catalog, so it cannot drift from what
// the validator accepts.
func PromptSnippet() string {
	var b strings.Builder
	b.WriteString("\n## Showing the user a small interface (ui_render)\n")
	b.WriteString("Use ui_render only when the user must fill in or pick something, or press a button to decide: a short form, a choice, a confirmation. ")
	b.WriteString("Plain answers, explanations, code and lists stay ordinary markdown; never use a surface for them.\n")
	b.WriteString("Several questions go on one surface with one button that sends every answer: never a surface or a button per question.\n")
	b.WriteString("ui_render takes {messages: [...]}, each an A2UI v1.0 envelope {\"version\":\"1.0\", \"<kind>\": {...}} with kind createSurface, updateComponents, updateDataModel or deleteSurface. ")
	b.WriteString("createSurface needs a surfaceId (a short token, no spaces or slashes) and may carry components and a dataModel; surfaceId is repeated in every later message of that surface. ")
	fmt.Fprintf(&b, "A surfaceId never starts with %s (reserved). ", strings.Join(SystemSurfacePrefixes, ", "))
	b.WriteString("Payloads: createSurface {surfaceId, components?, dataModel?}; updateComponents {surfaceId, components}; updateDataModel {surfaceId, path, value}; deleteSurface {surfaceId}. No other fields. ")
	b.WriteString("Components are flat objects {\"id\":..., \"component\":<Name>, ...props}; children are referenced by id, never nested, and each component has at most one parent (give every button its own label component). One component must have id \"root\". Keep a surface small.\n")
	b.WriteString("Components (* = required; dyn = a literal or a binding):\n")
	names := Components()
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(&b, "- %s: %s\n", n, describeComponent(catalog[n]))
	}
	b.WriteString("Binding: a dyn value may be {\"@path\":\"/form/name\"}, a JSON pointer into the surface's data model; input components (TextField, CheckBox, ChoicePicker) bind their value there, so give them a path and seed it with updateDataModel or createSurface dataModel. ")
	b.WriteString("ChoicePicker options are [{\"label\":..., \"value\":...}].\n")
	b.WriteString("A Button's action is {\"event\":{\"name\":\"submit_form\", \"context\":{...}}}; name is a token of your choice, but never starts with hil., mission. or system. (reserved). ")
	b.WriteString("context values may be literals or bindings, resolved when the user presses the button.\n")
	l := DefaultLimits
	fmt.Fprintf(&b, "Limits: %d messages per call, %d surfaces with buttons awaiting a press, %d components per surface, %d children per container, %d levels of nesting, %d options per picker, %d bytes per string. Over any of them the call is rejected; show less, or answer in markdown.\n",
		MaxMessagesPerCall, MaxSurfaces, l.MaxComponents, l.MaxChildren, l.MaxDepth, l.MaxOptions, l.MaxStringBytes)
	b.WriteString("When the user presses a button, the result arrives as the next message to you, as JSON with the action name, the surface and the context. Do not wait for it inside the turn; say what you are waiting for and end your turn. ")
	b.WriteString("A rejected ui_render tells you why: fix and resend, and after repeated failures answer in markdown instead.\n")
	return b.String()
}

func describeComponent(c compSpec) string {
	req := map[string]bool{}
	for _, r := range c.required {
		req[r] = true
	}
	keys := make([]string, 0, len(c.props))
	for k := range c.props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		p := k
		if req[k] {
			p += "*"
		}
		parts = append(parts, p+" "+describeProp(c.props[k]))
	}
	if len(parts) == 0 {
		return "(no properties)"
	}
	return strings.Join(parts, "; ")
}

func describeProp(p propSpec) string {
	switch p.kind {
	case pDynString:
		return "(dyn string)"
	case pDynNumber:
		return "(dyn number)"
	case pDynBool:
		return "(dyn boolean)"
	case pDynStringList:
		return "(dyn list of strings)"
	case pChild:
		return "(component id)"
	case pChildren:
		return "(list of component ids)"
	case pEnum:
		return "(" + strings.Join(p.enum, "|") + ")"
	case pAction:
		return "(action)"
	case pOptions:
		return "(options)"
	case pBool:
		return "(boolean)"
	}
	return ""
}
