package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/dynorg"
)

// TestEvalDir judges the saved answers in $A2UI_EVAL_DIR and writes the system
// prompt to $A2UI_EVAL_PROMPT_OUT; it skips when neither is set. Give both as
// absolute paths and run with -count=1 -v.
func TestEvalDir(t *testing.T) {
	dir, out := os.Getenv("A2UI_EVAL_DIR"), os.Getenv("A2UI_EVAL_PROMPT_OUT")
	if dir == "" && out == "" {
		t.Skip("set A2UI_EVAL_DIR and/or A2UI_EVAL_PROMPT_OUT")
	}
	if out != "" {
		if err := os.WriteFile(out, []byte(Prompt()), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote the system prompt to %s", out)
	}
	if dir == "" {
		return
	}
	results, err := Dir(dir)
	if err != nil {
		t.Fatal(err)
	}
	report, failed := Report(results)
	t.Log("\n" + report)
	if failed > 0 {
		t.Errorf("%d of %d answers failed", failed, len(results))
	}
}

const good = `{"version":"1.0","createSurface":{"surfaceId":"s","components":[
 {"id":"root","component":"Column","children":["b"]},{"id":"l","component":"Text","text":"Go"},
 {"id":"b","component":"Button","child":"l","action":{"event":{"name":"go","context":{}}}}]}}`

func TestDirJudgesEachFile(t *testing.T) {
	dir := t.TempDir()
	reserved := strings.Replace(good, `"name":"go"`, `"name":"hil.approve"`, 1)
	files := map[string]string{
		"confirm_button-a.json": `{"messages":[` + good + `]}`,
		"short_form.json":       "Here you go:\n```json\n{\"messages\":[" + good + "]}\n```\nDone {not json}.",
		"x.json":                `{"task":"no_surface","messages":[]}`,
		"no_surface-b.json":     `{"messages":[` + good + `]}`,
		"choice_question.json":  `{"messages":[]}`,
		"long_list.json":        `{"messages":[]}`,
		"reserved_name.json":    `{"messages":[` + reserved + `]}`,
		"noroot.json":           `{"messages":[{"version":"1.0","createSurface":{"surfaceId":"s"}}]}`,
		"prose.json":            "I would rather explain it. {\"a\":1}",
		"many.json":             `{"messages":[` + strings.TrimSuffix(strings.Repeat(good+",", a2ui.MaxMessagesPerCall+1), ",") + `]}`,
		"null.json":             `{"messages":null}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	results, err := Dir(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{ // "" passes, else a piece of the error
		"confirm_button-a.json": "", "short_form.json": "", "x.json": "", "long_list.json": "",
		"no_surface-b.json": "not appropriate", "choice_question.json": "needs a surface",
		"reserved_name.json": "reserved", "noroot.json": "root", "prose.json": "no JSON object",
		"many.json": "at most", "null.json": "not an array",
	}
	if len(results) != len(want) {
		t.Fatalf("%d results", len(results))
	}
	for _, r := range results {
		w := want[r.File]
		if (w == "") != (r.Err == nil) || (w != "" && !strings.Contains(r.Err.Error(), w)) {
			t.Errorf("%s: err %v, want %q", r.File, r.Err, w)
		}
	}
	report, failed := Report(results)
	if failed != 7 || !strings.Contains(report, "summary: 4/11 passed, 7 failed") {
		t.Errorf("failed %d\n%s", failed, report)
	}
	if _, err := Dir(t.TempDir()); err == nil {
		t.Error("an empty directory passed")
	}
}

func TestTasksAndPrompt(t *testing.T) {
	tasks, err := Tasks()
	if err != nil || len(tasks) != 8 {
		t.Fatalf("%d tasks, %v", len(tasks), err)
	}
	seen := map[string]bool{}
	for _, k := range tasks {
		if seen[k.ID] || k.Prompt == "" || (k.Expect != ExpectSurface && k.Expect != ExpectNone && k.Expect != ExpectEither) {
			t.Errorf("task %+v", k)
		}
		seen[k.ID] = true
	}
	if p := Prompt(); !strings.HasPrefix(p, a2ui.PromptSnippet()) || !strings.Contains(p, `{"messages":[...]}`) {
		t.Error("the prompt is not the snippet followed by the instruction")
	}
}

// The prompt states the tool's own message cap.
func TestMessageCapMatchesTheTool(t *testing.T) {
	if a2ui.MaxMessagesPerCall != dynorg.MaxUIMessages {
		t.Errorf("prompt says %d messages per call, ui_render takes %d", a2ui.MaxMessagesPerCall, dynorg.MaxUIMessages)
	}
}
