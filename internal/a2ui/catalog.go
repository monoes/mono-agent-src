package a2ui

type propKind int

const (
	pDynString propKind = iota // string | {"@path": ...}
	pDynNumber
	pDynBool
	pDynStringList
	pChild    // one component id
	pChildren // array of component ids
	pEnum
	pAction
	pOptions
	pBool // plain boolean
)

type propSpec struct {
	kind propKind
	enum []string
}

type compSpec struct {
	required []string
	props    map[string]propSpec
}

var (
	justifyEnum = []string{"start", "center", "end", "spaceBetween", "spaceAround", "spaceEvenly", "stretch"}
	alignEnum   = []string{"start", "center", "end", "stretch"}
)

// catalog is our trusted component set: the basic-catalog components we
// need, plus two of our own (Progress, Badge). Nothing outside it renders.
var catalog = map[string]compSpec{
	"Text": {[]string{"text"}, map[string]propSpec{
		"text": {kind: pDynString}, "variant": {pEnum, []string{"caption", "body"}}}},
	"Row": {[]string{"children"}, map[string]propSpec{
		"children": {kind: pChildren}, "justify": {pEnum, justifyEnum}, "align": {pEnum, alignEnum}}},
	"Column": {[]string{"children"}, map[string]propSpec{
		"children": {kind: pChildren}, "justify": {pEnum, justifyEnum}, "align": {pEnum, alignEnum}}},
	"Card":    {[]string{"child"}, map[string]propSpec{"child": {kind: pChild}}},
	"Divider": {nil, map[string]propSpec{"axis": {pEnum, []string{"horizontal", "vertical"}}}},
	"Button": {[]string{"child", "action"}, map[string]propSpec{
		"child": {kind: pChild}, "action": {kind: pAction},
		"variant": {pEnum, []string{"default", "primary", "borderless", "danger"}}}},
	"TextField": {[]string{"label", "value"}, map[string]propSpec{
		"label": {kind: pDynString}, "value": {kind: pDynString}, "placeholder": {kind: pDynString},
		"variant": {pEnum, []string{"shortText", "longText", "number"}}}},
	"CheckBox": {[]string{"label", "value"}, map[string]propSpec{
		"label": {kind: pDynString}, "value": {kind: pDynBool}}},
	"ChoicePicker": {[]string{"options", "value"}, map[string]propSpec{
		"options": {kind: pOptions}, "value": {kind: pDynStringList},
		"variant":      {pEnum, []string{"multipleSelection", "mutuallyExclusive"}},
		"displayStyle": {pEnum, []string{"checkbox", "chips"}}, "filterable": {kind: pBool}}},
	"Progress": {[]string{"value"}, map[string]propSpec{
		"value": {kind: pDynNumber}, "max": {kind: pDynNumber}, "label": {kind: pDynString}}},
	"Badge": {[]string{"text"}, map[string]propSpec{
		"text": {kind: pDynString}, "tone": {pEnum, []string{"neutral", "info", "success", "warning", "danger"}}}},
}

// Components lists the component names of the catalog, for building the
// agent's prompt snippet.
func Components() []string {
	out := make([]string, 0, len(catalog))
	for k := range catalog {
		out = append(out, k)
	}
	return out
}
