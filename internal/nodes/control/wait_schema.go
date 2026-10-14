package control

// WaitNodeSchema documents the config keys WaitNode.Execute reads out of its
// map[string]interface{} config — see SetNodeSchema's doc comment for why
// this is a companion struct rather than the runtime config, and
// internal/tools/schemagen for the tag grammar.
//
// "duration" is the original seconds-based form, chosen by resume
// "duration" (or no resume at all, as older workflows have); the other
// resume values select the n8n-style alternatives. Waits longer than a minute pause the execution
// durably, so there is no upper bound. webhook and form wait for the
// execution's resume URL ($execution.resumeUrl) to be called or answered.
// formFields' options list the fieldType values the form serves, plus
// multiselect (a dropdown may take several choices); the GUI form builder
// offers exactly those.
type WaitNodeSchema struct {
	Duration float64 `json:"duration" schema:"label=Wait Duration (seconds),type=number,default=5,min=1,depends_on_key=resume,depends_on_values=duration|"`
	Resume   string  `json:"resume" schema:"label=Resume,type=select,options=duration|timeInterval|specificTime|webhook|form,default=duration,help=duration waits Wait Duration seconds; timeInterval waits Amount × Unit; specificTime waits until Date and Time; webhook waits for a call to $execution.resumeUrl; form waits for the form at that URL to be answered."`
	Amount   float64 `json:"amount" schema:"label=Amount,type=number,default=1,min=0,depends_on_key=resume,depends_on_values=timeInterval"`
	Unit     string  `json:"unit" schema:"label=Unit,type=select,options=seconds|minutes|hours|days,default=hours,depends_on_key=resume,depends_on_values=timeInterval"`
	DateTime string  `json:"dateTime" schema:"label=Date and Time,type=text,placeholder=2026-12-31T09:00:00Z,help=RFC 3339 or local YYYY-MM-DD HH:MM.,depends_on_key=resume,depends_on_values=specificTime"`

	HTTPMethod      string  `json:"httpMethod" schema:"label=HTTP Method,type=select,options=|GET|POST|PUT|PATCH|DELETE,help=The method the resume call must use; empty accepts any.,depends_on_key=resume,depends_on_values=webhook"`
	FormTitle       string  `json:"formTitle" schema:"label=Form Title,type=text,depends_on_key=resume,depends_on_values=form"`
	FormDescription string  `json:"formDescription" schema:"label=Form Description,type=textarea,depends_on_key=resume,depends_on_values=form"`
	FormFields      string  `json:"formFields" schema:"label=Form Fields,type=code,language=json,rows=6,options=text|textarea|number|email|password|date|dropdown|radio|checkbox|multiselect,placeholder=[{\"fieldLabel\": \"Name\"， \"requiredField\": true}],help=A list of {fieldLabel， fieldName， fieldType (text|textarea|number|email|password|date|dropdown|radio|checkbox)， requiredField， placeholder， fieldOptions: {values: [{option}]}， multiselect}.,depends_on_key=resume,depends_on_values=form"`
	ButtonLabel     string  `json:"buttonLabel" schema:"label=Button Label,type=text,placeholder=Submit,depends_on_key=resume,depends_on_values=form"`
	LimitWaitTime   bool    `json:"limitWaitTime" schema:"label=Limit Wait Time,type=boolean,default=false,help=Give up waiting after a while and continue with the input items.,depends_on_key=resume,depends_on_values=webhook|form"`
	LimitType       string  `json:"limitType" schema:"label=Limit Type,type=select,options=afterTimeInterval|atSpecifiedTime,default=afterTimeInterval,help=Used when Limit Wait Time is on.,depends_on_key=resume,depends_on_values=webhook|form"`
	ResumeAmount    float64 `json:"resumeAmount" schema:"label=Limit Amount,type=number,default=1,min=0,help=Used with limit type afterTimeInterval.,depends_on_key=resume,depends_on_values=webhook|form"`
	ResumeUnit      string  `json:"resumeUnit" schema:"label=Limit Unit,type=select,options=seconds|minutes|hours|days,default=hours,depends_on_key=resume,depends_on_values=webhook|form"`
	MaxDateAndTime  string  `json:"maxDateAndTime" schema:"label=Limit Date and Time,type=text,placeholder=2026-12-31T09:00:00Z,help=Used with limit type atSpecifiedTime.,depends_on_key=resume,depends_on_values=webhook|form"`
}
