package browserjev

// ReadNodeSchema documents the config keys ReadNode.Execute reads (see
// NodeSchema; generated into internal/workflow/schemas/browser.read.json).
type ReadNodeSchema struct {
	URL string `json:"url" schema:"label=URL,type=text,required,placeholder=https://example.com,help=Opened in a new tab of the browser bound to this profile and closed once read. Nothing on the page is clicked or typed."`

	MaxChars float64 `json:"max_chars" schema:"label=Max Characters,type=number,default=20000,help=The text is cut at this many characters (at most 200000)."`
}
