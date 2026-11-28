// Package eval checks what models emit for the ui_render tool: it reads saved
// answers from a directory and runs each through the same validator the tool
// uses. It is a test harness only; no binary imports it.
package eval

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/a2ui"
)

//go:embed tasks.json
var tasksJSON []byte

// What a task expects of the answer.
const (
	ExpectSurface = "surface" // at least one live surface
	ExpectNone    = "none"    // {"messages":[]}: a surface is not appropriate
	ExpectEither  = "either"  // a valid surface, or no surface (markdown)
)

// Task is one request to put to a model.
type Task struct {
	ID     string `json:"id"`
	Expect string `json:"expect"`
	Prompt string `json:"prompt"`
}

// Tasks returns the built-in tasks (tasks.json).
func Tasks() ([]Task, error) {
	var t []Task
	if err := json.Unmarshal(tasksJSON, &t); err != nil {
		return nil, fmt.Errorf("tasks.json: %w", err)
	}
	return t, nil
}

// instruction turns the tool prompt into one a model without tools can answer.
const instruction = "\n## How to answer\n" +
	"You have no tools here. Instead of calling ui_render, reply with only one JSON object {\"messages\":[...]}, " +
	"the argument you would have passed to it. If a surface is not appropriate for the request, reply {\"messages\":[]}. " +
	"No other text and no code fence.\n"

// Prompt is the system prompt to give the model under test.
func Prompt() string { return a2ui.PromptSnippet() + instruction }

// Result is the verdict on one answer file.
type Result struct {
	File     string
	Task     string // the matched task id, "" when the file names none
	Surfaces int    // surfaces left live
	Err      error  // nil: pass
}

// Answer reads the messages of one saved answer: a JSON object with a
// "messages" array, alone ({"task": ..., "messages": [...]}) or inside model
// text, in a code fence or not. The first such object wins. task is its "task"
// string, if any.
func Answer(data []byte) (task string, messages []json.RawMessage, err error) {
	text := string(data)
	for i := strings.IndexByte(text, '{'); i >= 0; {
		var obj map[string]json.RawMessage
		if json.NewDecoder(strings.NewReader(text[i:])).Decode(&obj) == nil {
			if raw, ok := obj["messages"]; ok {
				if err := json.Unmarshal(raw, &messages); err != nil || messages == nil {
					return "", nil, errors.New(`"messages" is not an array`)
				}
				_ = json.Unmarshal(obj["task"], &task)
				return task, messages, nil
			}
		}
		next := strings.IndexByte(text[i+1:], '{')
		if next < 0 {
			break
		}
		i += 1 + next
	}
	return "", nil, errors.New(`no JSON object with a "messages" array`)
}

// Validate runs the messages as one ui_render call of an agent on a fresh
// registry: the call's message cap, every message through the registry, then
// Check on every surface left live. It returns how many are left.
func Validate(messages []json.RawMessage) (int, error) {
	if len(messages) > a2ui.MaxMessagesPerCall {
		return 0, fmt.Errorf("at most %d messages per call, got %d", a2ui.MaxMessagesPerCall, len(messages))
	}
	reg := a2ui.NewRegistry(a2ui.DefaultLimits)
	var ids []string
	for i, raw := range messages {
		m, err := reg.Handle(raw, a2ui.FromAgent)
		if err != nil {
			return 0, fmt.Errorf("messages[%d]: %w", i, err)
		}
		ids = append(ids, m.SurfaceID)
	}
	live := map[string]bool{}
	for _, id := range ids {
		s := reg.Surface(id)
		if s == nil || live[id] {
			continue
		}
		if err := s.Check(a2ui.DefaultLimits); err != nil {
			return 0, fmt.Errorf("surface %q: %w", id, err)
		}
		live[id] = true
	}
	return len(live), nil
}

// matchTask finds the task of an answer: by its "task" string (an id or the
// prompt itself), else by a file name that starts with the id, alone or
// followed by "." or "-" (choice_question-modelx.json).
func matchTask(tasks []Task, named, file string) *Task {
	base := strings.TrimSuffix(file, filepath.Ext(file))
	for i, t := range tasks {
		if named != "" && (named == t.ID || named == t.Prompt) {
			return &tasks[i]
		}
	}
	for i, t := range tasks {
		if base == t.ID || strings.HasPrefix(base, t.ID+".") || strings.HasPrefix(base, t.ID+"-") {
			return &tasks[i]
		}
	}
	return nil
}

// File judges one answer file against the validator and its task's expectation.
func File(tasks []Task, path string) Result {
	res := Result{File: filepath.Base(path)}
	data, err := os.ReadFile(path)
	if err != nil {
		res.Err = err
		return res
	}
	named, messages, err := Answer(data)
	t := matchTask(tasks, named, res.File)
	if t != nil {
		res.Task = t.ID
	}
	if err != nil {
		res.Err = err
		return res
	}
	res.Surfaces, res.Err = Validate(messages)
	switch {
	case res.Err != nil || t == nil:
	case t.Expect == ExpectNone && len(messages) > 0:
		res.Err = fmt.Errorf(`a surface is not appropriate for this task: expected {"messages":[]}, got %d messages`, len(messages))
	case t.Expect == ExpectSurface && res.Surfaces == 0:
		res.Err = errors.New("the task needs a surface and none is left live")
	}
	return res
}

// Dir judges every *.json file of a directory, in name order.
func Dir(dir string) ([]Result, error) {
	tasks, err := Tasks()
	if err != nil {
		return nil, err
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no *.json files in %q", dir)
	}
	sort.Strings(files)
	out := make([]Result, len(files))
	for i, f := range files {
		out[i] = File(tasks, f)
	}
	return out, nil
}

// Report is one line per file and a summary line. Text from an answer is
// cleaned before it is printed.
func Report(results []Result) (report string, failed int) {
	var b strings.Builder
	for _, r := range results {
		task := r.Task
		if task == "" {
			task = "no task"
		}
		if r.Err != nil {
			failed++
			fmt.Fprintf(&b, "FAIL %s (%s): %s\n", a2ui.CleanLine(r.File), task, a2ui.CleanLine(r.Err.Error()))
			continue
		}
		fmt.Fprintf(&b, "PASS %s (%s): %d surfaces\n", a2ui.CleanLine(r.File), task, r.Surfaces)
	}
	fmt.Fprintf(&b, "summary: %d/%d passed, %d failed\n", len(results)-failed, len(results), failed)
	return b.String(), failed
}
