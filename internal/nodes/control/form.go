package control

import (
	"context"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// FormNode is n8n's Form node: the next page, or the ending, of the browser
// session a trigger.form submission started (workflow/form_pages.go). The
// person who submitted the form is taken to each page in turn on the
// execution's resume URL; IF and Switch between pages pick which page comes
// next.
//
// Config fields (n8n's names):
//
//	"operation"       page (default) or completion
//	page:
//	"formFields"      a list (or {values: [...]}, or that as JSON text) of
//	                  {fieldLabel, fieldName, fieldType, requiredField,
//	                  placeholder, fieldOptions: {values: [{option}]},
//	                  multiselect}; "defineForm": "json" reads "jsonOutput"
//	"formTitle", "formDescription", "buttonLabel" the page (also read
//	                  from "options")
//	"limitWaitTime"   true to give up: "limitType" afterTimeInterval
//	                  ("resumeAmount" × "resumeUnit") or atSpecifiedTime
//	                  ("maxDateAndTime")
//	completion:
//	"respondWith"     text (default: "completionTitle" and
//	                  "completionMessage"), redirect ("redirectUrl") or
//	                  showText ("responseText", shown as the page)
//
// Outputs: a page — one item with the answers keyed by field, submittedAt
// and formMode, as n8n's page outputs; past the limit, the input items. An
// ending passes its input items on.
type FormNode struct{}

func (n *FormNode) Type() string { return "core.form" }

func (n *FormNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	pass := []workflow.NodeOutput{{Handle: "main", Items: input.Items}}
	switch op, _ := config["operation"].(string); op {
	case "", "page":
	case "completion":
		if err := workflow.EndFormFlow(ctx, input, formEndingSpec(config)); err != nil {
			return nil, fmt.Errorf("form: %w", err)
		}
		return pass, nil
	default:
		return nil, fmt.Errorf("%w: form \"operation\" must be page or completion, got %q", workflow.ErrInvalidConfig, op)
	}
	res, err := workflow.WaitReplyAnswer(ctx, input)
	if err != nil {
		return nil, err
	}
	if res == nil {
		spec, err := formPageSpec(config, time.Now())
		if err != nil {
			return nil, err
		}
		reply, err := workflow.NewWaitReply(ctx, input, spec)
		if err != nil {
			return nil, err
		}
		return nil, reply.Pause()
	}
	switch res.Status {
	case workflow.WaitReplyPending:
		return nil, res.Pause()
	case workflow.WaitReplyExpired:
		return pass, nil
	}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{{JSON: res.Answer}}}}, nil
}

// formOption reads key from the config, or from its "options" (where n8n
// keeps a page's title, description and button label).
func formOption(config map[string]interface{}, key string) string {
	if s, _ := config[key].(string); s != "" {
		return s
	}
	opts, _ := config["options"].(map[string]interface{})
	s, _ := opts[key].(string)
	return s
}

// formPageSpec builds the reply a form page opens.
func formPageSpec(config map[string]interface{}, now time.Time) (workflow.WaitReplySpec, error) {
	spec := workflow.WaitReplySpec{Kind: workflow.WaitReplyFormPage, ExecutionURL: true,
		Title: formOption(config, "formTitle"), Message: formOption(config, "formDescription"),
		ButtonLabel: formOption(config, "buttonLabel")}
	raw := config["formFields"]
	if d, _ := config["defineForm"].(string); d == "json" {
		raw = config["jsonOutput"]
	}
	fields, err := workflow.ParseReplyFormFields(raw)
	if err != nil {
		return spec, fmt.Errorf("form: %w", err)
	}
	if len(fields) == 0 {
		return spec, fmt.Errorf("%w: form page \"formFields\" needs at least one field", workflow.ErrInvalidConfig)
	}
	spec.FormFields = fields
	if on, _ := config["limitWaitTime"].(bool); on {
		limit, err := workflow.ParseReplyLimit(config, now)
		if err != nil {
			return spec, fmt.Errorf("form: %w", err)
		}
		spec.Limit = limit
	}
	return spec, nil
}

// formEndingSpec reads a form ending's config.
func formEndingSpec(config map[string]interface{}) workflow.WaitReplySpec {
	str := func(k string) string { s, _ := config[k].(string); return s }
	spec := workflow.WaitReplySpec{RespondWith: str("respondWith"), RedirectURL: str("redirectUrl"),
		Title: str("completionTitle"), Message: str("completionMessage"), ResponseText: str("responseText")}
	if spec.Title == "" {
		spec.Title = formOption(config, "formTitle")
	}
	return spec
}
