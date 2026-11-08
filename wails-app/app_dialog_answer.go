package main

import (
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// dialogConfirmed reports whether choice, the answer of a native dialog of type typ on goos, is the person confirming. It is
// the ONLY place that reads a confirmation (TestDialogCallersReadTheAnswerThroughTheHelper): Wails v2.17.0 honours custom
// Buttons on macOS only and returns the clicked button's text there; on Linux (GTK) a Question answers "Yes"/"No" and a
// Warning "OK" (Windows says "Ok"; compared without case), on Windows (MessageBox) the standard names, whatever Buttons say. button is the confirming button's text
// (used on macOS). Any other type or answer is not a confirmation.
func dialogConfirmed(goos string, typ wailsruntime.DialogType, button, choice string) bool {
	if typ != wailsruntime.QuestionDialog && typ != wailsruntime.WarningDialog {
		return false
	}
	if choice == "" { // no answer is never a confirmation, whatever the button label is
		return false
	}
	if goos == "darwin" {
		return button != "" && choice == button
	}
	if typ == wailsruntime.QuestionDialog {
		return choice == "Yes"
	}
	return strings.EqualFold(choice, "ok")
}
