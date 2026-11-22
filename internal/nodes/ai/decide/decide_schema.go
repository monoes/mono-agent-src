package decide

// DecideNodeSchema documents the config keys Node.Execute reads — see
// internal/nodes/control.SetNodeSchema's doc comment for why this is a
// companion struct, and internal/tools/schemagen for the tag grammar.
//
// Output handles: noul → yes， no; score → level_0 … level_<n-1> (the most
// probable level); choice → the option ids; then low_confidence. Each item
// keeps its fields and gains decision: {kind， answer， handle， probability，
// probabilities， score， confidence， low_confidence， provider， model，
// probability_kind}. One failed item request fails the whole node.
type DecideNodeSchema struct {
	Kind string `json:"kind" schema:"label=Kind,type=select,required,options=noul|score|choice,help=noul: a yes/no question. score: a level on an ordered rubric. choice: one of your options."`

	Question string `json:"question" schema:"label=Question,type=textarea,rows=3,required,help=What to decide about each item. Item content is always treated as data， never instructions."`

	Input string `json:"input" schema:"label=Input,type=textarea,rows=3,placeholder={{$json.text}},help=What the provider reads per item. Supports {{$json.field}} placeholders. Empty = the fields below， or the whole item. Capped at 6000 characters."`

	Fields string `json:"fields" schema:"label=Fields,type=array,item_type=text,help=Item keys to send when Input is empty (a projection of the item)."`

	CriteriaTrue string `json:"criteria_true" schema:"label=Yes Means,type=text,depends_on_key=kind,depends_on_values=noul,help=Optional: what a yes stands for."`

	CriteriaFalse string `json:"criteria_false" schema:"label=No Means,type=text,depends_on_key=kind,depends_on_values=noul,help=Optional: what a no stands for."`

	Levels string `json:"levels" schema:"label=Levels,type=array,item_type=text,depends_on_key=kind,depends_on_values=score,help=2 to 10 level descriptions， lowest first. Level i goes out on handle level_i."`

	Options string `json:"options" schema:"label=Options (JSON),type=code,language=json,rows=4,depends_on_key=kind,depends_on_values=choice,placeholder={\"refund\": \"asks for money back\"， \"bug\": \"reports a defect\"},help={id: description} or a list of ids. Each id becomes an output handle."`

	MinConfidence float64 `json:"min_confidence" schema:"label=Min Probability,type=number,default=0.6,min=0,max=1,help=Items whose answer's probability is below this go to the low_confidence handle (for yes/no the probability of the answer given)."`

	Provider string "json:\"provider\" schema:\"label=Decision Provider,type=text,help=A decision provider added with `monoagentcli jev provider add`. Empty = the default.\""

	Model string `json:"model" schema:"label=Model,type=text,help=Empty = the provider's model. A provider other than TypeSafe takes it only when this node also names that provider."`
}
