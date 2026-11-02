// wails-app/app_person_ask.go
//
// The person's answer to a critical ask of a lead (an ancestor_question item): `monoagentcli --profile <active>
// --json task session person answer-ask --ask=ID --text=TEXT`, the CLI's document verbatim. Who may answer and
// what the answer does are the CLI's rules; the text is another person's words to an agent, and the question it
// answers is untrusted text the CLI lists (the GUI renders it as text).
package main

import (
	"strconv"
	"strings"
)

// PersonItemAnswerAsk is `task session person answer-ask --ask=ID --text=TEXT`.
func (a *App) PersonItemAnswerAsk(askID int64, text string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if askID <= 0 {
		return taskRefusal("an ask id is a positive number")
	}
	if strings.TrimSpace(text) == "" {
		return taskRefusal("write your words first")
	}
	return a.taskCLI("", "session", "person", "answer-ask", "--ask="+strconv.FormatInt(askID, 10), "--text="+text)
}
