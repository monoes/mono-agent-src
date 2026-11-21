package browserjev

// NodeSchema documents the config keys Node.Execute reads — see
// internal/nodes/control.SetNodeSchema's doc comment for why this is a
// companion struct, and internal/tools/schemagen for the tag grammar.
type NodeSchema struct {
	URL string `json:"url" schema:"label=Start URL,type=text,required,help=Opened in a new tab of your connected browser."`

	Goal string `json:"goal" schema:"label=Goal,type=textarea,required,rows=4,help=What to achieve in plain language — include every value to enter. The run stops at DONE or BLOCKED."`

	Provider string "json:\"provider\" schema:\"label=Decision Provider,type=text,help=A decision provider added with `monoagentcli jev provider add`. Empty = the default.\""

	APIKey string "json:\"api_key\" schema:\"label=TypeSafe API Key,type=password,default=@secret:typesafe,help=Applies to the TypeSafe provider. Falls back to the profile's vault secret `typesafe`， then TYPESAFE_API_KEY.\""

	Model string `json:"model" schema:"label=Jev Model,type=text,default=jev-latest,help=Applies to the TypeSafe provider (another provider uses its own model unless you set one here)."`

	Values string `json:"values" schema:"label=Values,type=code,language=json,rows=5,placeholder={\"From\": \"Zurich\"， \"email\": \"@secret:my-email\"},help=Name → value pairs typed into matching fields instead of asking the text runtime. Jev sees only the names (typed values are shown to it as <value:NAME>). @secret: values need an email/tel field or one whose label names the value (password inputs are never observed)."`

	Files string `json:"files" schema:"label=Files,type=code,language=json,rows=3,placeholder=[\"~/Documents/cv.pdf\"],help=Paths of files from this machine. Each time a click on an Upload button asks for a file chooser it is answered with the next file (an input that takes several gets all that are left) and the native dialog never opens. A run an AI started may only do this on local pages or at full-no-approvals； elsewhere use monoagentcli browser upload."`

	ProfileInfo bool `json:"profile_info" schema:"label=Use Profile Info,type=boolean,help=Adds the profile's Info page (first_name， email， address…) to the values by key. ID and bank numbers are kept as secrets and photos and the card are never used. Values you list yourself win."`

	TextRuntime string `json:"text_runtime" schema:"label=Text Runtime,type=text,default=claude,help=Local agent that writes TYPE_TEXT values (monoagentcli agent scan --installed)."`

	TextModel string `json:"text_model" schema:"label=Text Model,type=text,help=Model for the text runtime； a small one (e.g. haiku) keeps runs fast."`

	MaxActions float64 `json:"max_actions" schema:"label=Max Actions,type=number,default=40,help=Browser actions per run (decisions are capped at twice this)."`

	Timeout float64 `json:"timeout" schema:"label=Timeout (seconds),type=number,default=300"`

	FailOnBlocked bool `json:"fail_on_blocked" schema:"label=Fail When Not Done,type=boolean,help=Error the node unless the run ends DONE (otherwise status is on the item)."`
}
