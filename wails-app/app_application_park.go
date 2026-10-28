// wails-app/app_application_park.go
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ParkedTab is the tab a fill left open for the person to submit in.
type ParkedTab struct {
	ApplicationID string `json:"applicationId"`
	TaskID        string `json:"taskId"`
	TabID         int    `json:"tabId"`
	Origin        string `json:"origin"`
	State         string `json:"state"`
	UpdatedAt     string `json:"updatedAt"`
}

// ApplicationTab is `application fill tab <id>`: the parked tab (nil when no
// fill left one) and when the extension saw a submit in it. That is only a
// hint to ask the person; the application stays awaiting_submit until they
// confirm it.
type ApplicationTab struct {
	ApplicationID    string     `json:"applicationId"`
	Tab              *ParkedTab `json:"tab"`
	SubmitObservedAt string     `json:"submitObservedAt"`
}

// SweepResult is `application fill sweep`.
type SweepResult struct {
	Closed []string `json:"closed"`
	Lost   []string `json:"lost"`
}

// applicationQueueKey names the profile's one running fill queue.
func applicationQueueKey(profile string) string { return "application-fill-queue:" + profile }

// GetApplicationTab is `application fill tab <id>`.
func (a *App) GetApplicationTab(id string) (ApplicationTab, error) {
	var out ApplicationTab
	if err := a.signedOut(); err != nil {
		return out, err
	}
	err := a.runMonoCLI("", &out, "application", "fill", "tab", id)
	return out, err
}

// ConfirmApplicationSubmitted records that the person submitted the
// application themselves. It is SendApplication, the one existing path to
// applied; the fill never reaches it.
func (a *App) ConfirmApplicationSubmitted(id string) error {
	return a.SendApplication(id, "")
}

// ResumeApplicationFill is `application fill resume <id>`: a new task for an
// application whose tab is parked or lost. coder_disabled comes back as the
// oneshot fallback, like StartApplicationFill.
func (a *App) ResumeApplicationFill(id string) (AITask, error) {
	if err := a.signedOut(); err != nil {
		return AITask{}, err
	}
	full, err := a.scopedArgs("application", "fill", "resume", id)
	if err != nil {
		return AITask{}, err
	}
	var res struct {
		TaskID string `json:"taskId"`
	}
	if err := a.jsonCLIInto(&res, full); err != nil {
		var ae *aitaskError
		if errors.As(err, &ae) && ae.code == "coder_disabled" {
			return AITask{Fallback: "oneshot", Reason: ae.msg}, nil
		}
		return AITask{}, err
	}
	// The resume output names the task, not its conversation: read it back.
	return a.GetAITask(res.TaskID)
}

// SweepApplicationTabs is `application fill sweep`.
func (a *App) SweepApplicationTabs() (SweepResult, error) {
	out := SweepResult{Closed: []string{}, Lost: []string{}}
	if err := a.signedOut(); err != nil {
		return out, err
	}
	err := a.runMonoCLI("", &out, "application", "fill", "sweep")
	return out, err
}

// StartApplicationQueue starts `application fill queue <ids> [--max-parked N]`
// as a supervised background process and returns at once: it fills the
// applications one by one, keeping at most maxParked running or waiting for
// the person's submit (0 = the saved setting, else 1 to 10). One queue runs
// per profile; progress shows in each application. It emits
// applications:queueStatus when the process ends.
func (a *App) StartApplicationQueue(ids []string, maxParked int) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return errors.New("pick at least one application")
	}
	if maxParked != 0 && (maxParked < 1 || maxParked > 10) {
		return fmt.Errorf("max parked must be 1 to 10, got %d", maxParked)
	}
	profile, err := a.profileID()
	if err != nil {
		return err
	}
	args := append([]string{"application", "fill", "queue"}, ids...)
	if maxParked != 0 {
		args = append(args, "--max-parked", strconv.Itoa(maxParked))
	}
	full, err := a.scopedArgs(args...)
	if err != nil {
		return err
	}
	bin, err := findMonoAgentCLI()
	if err != nil {
		return err
	}
	key := applicationQueueKey(profile)
	a.runningMu.Lock()
	defer a.runningMu.Unlock()
	if _, busy := a.runningCmds[key]; busy {
		return errors.New("a fill queue is already running for this profile")
	}
	cmd := exec.Command(bin, full...)
	suppressConsole(cmd)
	setChatProcessGroup(cmd)
	tail := &tailCapture{}
	var out bytes.Buffer // the outcomes line: one short entry per application
	cmd.Stdout = &out
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start fill queue: %w", err)
	}
	a.runningCmds[key] = cmd
	go func() {
		waitErr := waitChatProcess(cmd)
		a.runningMu.Lock()
		delete(a.runningCmds, key)
		a.runningMu.Unlock()
		status, message := "finished", ""
		if waitErr != nil {
			status, message = "error", orgRunFailureMessage(tail.String(), waitErr)
		} else if failed := queueFailures(out.Bytes()); failed != "" {
			status, message = "error", failed
		}
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "applications:queueStatus", map[string]any{"status": status, "message": message})
		}
	}()
	return nil
}

// queueFailures names why applications of a finished queue ended in error
// (the queue exits 0 with a failed outcome per application, whether the fill
// never started or ran and then failed); "" when none did.
func queueFailures(out []byte) string {
	var res struct {
		Outcomes []struct {
			Error string `json:"error"`
		} `json:"outcomes"`
	}
	if json.Unmarshal(bytes.TrimSpace(out), &res) != nil {
		return ""
	}
	n, first := 0, ""
	for _, o := range res.Outcomes {
		if o.Error == "" {
			continue
		}
		if n == 0 {
			first = o.Error
		}
		n++
	}
	switch n {
	case 0:
		return ""
	case 1:
		return "The fill queue stopped: " + first
	}
	return fmt.Sprintf("The fill queue stopped for %d applications; the first: %s", n, first)
}
