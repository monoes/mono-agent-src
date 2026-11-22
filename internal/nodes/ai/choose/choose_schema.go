package choose

// ChooseNodeSchema documents the config keys Node.Execute reads — see
// internal/nodes/control.SetNodeSchema's doc comment for why this is a
// companion struct, and internal/tools/schemagen for the tag grammar.
//
// cases accepts the same shapes as core.switch: a plain string (value and
// handle) or {value， handle， description}; description is the criterion
// Jev judges the case by. Output handles: one per case handle plus
// low_confidence. One failed item request fails the whole node (no partial
// output), so a re-run re-sends every item.
type ChooseNodeSchema struct {
	Cases string `json:"cases" schema:"label=Cases,type=array,item_type=text,required,help=Each value becomes an output handle. Objects {value， handle， description} set a custom handle and the criterion Jev judges by."`

	Input string `json:"input" schema:"label=Input,type=textarea,rows=3,placeholder={{$json.text}},help=What Jev reads per item. Supports {{$json.field}} placeholders. Empty = the fields below， or the whole item. Capped at 6000 characters."`

	Fields string `json:"fields" schema:"label=Fields,type=array,item_type=text,help=Item keys to send when Input is empty (a projection of the item)."`

	Instructions string `json:"instructions" schema:"label=Instructions,type=textarea,rows=3,help=Extra guidance for the choice. Item content is always treated as data， never instructions."`

	ExtraQuestions string `json:"extra_questions" schema:"label=Extra Questions (JSON),type=code,language=json,rows=5,placeholder={\"urgent\": {\"type\": \"noul\"， \"criteria\": \"Is this urgent?\"}},help=Answered in the same request: {name: {type: noul｜choice｜score， criteria}}. Written to <output_key>.extra."`

	MinConfidence float64 `json:"min_confidence" schema:"label=Min Probability,type=number,default=0.6,min=0,max=1,help=Items whose top option probability is below this go to the low_confidence handle."`

	OutputKey string `json:"output_key" schema:"label=Output Key,type=text,default=choice"`

	Provider string "json:\"provider\" schema:\"label=Decision Provider,type=text,help=A decision provider added with `monoagentcli jev provider add`. Empty = the default.\""

	APIKey string `json:"api_key" schema:"label=TypeSafe API Key,type=password,default=@secret:typesafe,help=Applies to the TypeSafe provider. Falls back to TYPESAFE_API_KEY."`

	Model string `json:"model" schema:"label=Jev Model,type=text,default=jev-latest,help=Applies to the TypeSafe provider (another provider uses its own model unless this node also names that provider)."`

	Concurrency float64 `json:"concurrency" schema:"label=Concurrency,type=number,default=4,min=1,max=8,help=Requests in flight at once (one request per item). If any item's request fails the whole node fails and a re-run sends every item again."`
}
