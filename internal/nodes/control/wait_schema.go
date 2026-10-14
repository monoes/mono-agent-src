package control

// WaitNodeSchema documents the config keys WaitNode.Execute reads out of its
// map[string]interface{} config — see SetNodeSchema's doc comment for why
// this is a companion struct rather than the runtime config, and
// internal/tools/schemagen for the tag grammar.
//
// "duration" is the original seconds-based form, chosen by resume
// "duration" (or no resume at all, as older workflows have); the other
// resume values select the n8n-style alternatives. Waits longer than a minute pause the execution
// durably, so there is no upper bound.
type WaitNodeSchema struct {
	Duration float64 `json:"duration" schema:"label=Wait Duration (seconds),type=number,default=5,min=1,depends_on_key=resume,depends_on_values=duration|"`
	Resume   string  `json:"resume" schema:"label=Resume,type=select,options=duration|timeInterval|specificTime,default=duration,help=duration waits Wait Duration seconds; timeInterval waits Amount × Unit; specificTime waits until Date and Time."`
	Amount   float64 `json:"amount" schema:"label=Amount,type=number,default=1,min=0,depends_on_key=resume,depends_on_values=timeInterval"`
	Unit     string  `json:"unit" schema:"label=Unit,type=select,options=seconds|minutes|hours|days,default=hours,depends_on_key=resume,depends_on_values=timeInterval"`
	DateTime string  `json:"dateTime" schema:"label=Date and Time,type=text,placeholder=2026-12-31T09:00:00Z,help=RFC 3339 or local YYYY-MM-DD HH:MM.,depends_on_key=resume,depends_on_values=specificTime"`
}
