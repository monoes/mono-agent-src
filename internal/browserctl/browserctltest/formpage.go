package browserctltest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/extension"
)

// FormPage stands in for the submit gate's page scripts (probe, hold,
// trigger) on a page with one form: its digest is a hash of Fields, so a
// changed value changes it the way the real in-page digest does. Install
// it with fake.On("eval_cdp", fp.Handler()).
type FormPage struct {
	mu       sync.Mutex
	Action   string
	Method   string
	Selector string
	// Submit: the probed control is submit-like.
	Submit bool
	Fields []browserctl.FormField
	// Triggered counts submits the trigger script let through.
	Triggered int
	// NoReaction: the page does not change after the click.
	NoReaction bool
	// Disabled: the submit control is disabled; Covered: another element
	// sits over it.
	Disabled, Covered bool
	// Blocks: the form changes between the trigger and the click, so the
	// page's click check stops the click. Misdirects: the click lands on
	// another element. Drifts: the form changed by the time of the check
	// right before the press. MovesAway: something covers the control by
	// then.
	Blocks, Misdirects, Drifts, MovesAway bool
	// StrayAndFired: a click elsewhere was cancelled, and the click on the
	// control went through.
	StrayAndFired bool
	// ReadFails: reading how the click went fails with this error;
	// OnRead runs first (a test moves the tab there).
	ReadFails string
	OnRead    func()
}

// Set changes one field's value.
func (fp *FormPage) Set(name, value string) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	for i := range fp.Fields {
		if fp.Fields[i].Name == name {
			fp.Fields[i].Value = value
		}
	}
}

func (fp *FormPage) digest() string {
	raw, _ := json.Marshal(fp.Fields)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (fp *FormPage) form() map[string]any {
	return map[string]any{"action": fp.Action, "method": fp.Method, "selector": fp.Selector, "fields": fp.Fields}
}

var expectRE = regexp.MustCompile(`const EXPECT = "([0-9a-f]*)";`)

// subRE finds the submit control's selector in the trigger script: "" when
// the held submit has none (the form is requestSubmit()ed).
var subRE = regexp.MustCompile(`const el = ("[^"]*") \?`)

// Handler answers eval_cdp for the three scripts; anything else gets null.
func (fp *FormPage) Handler() Handler {
	return func(c *extension.Command) (any, error) {
		fp.mu.Lock()
		defer fp.mu.Unlock()
		expr, _ := c.Params["expression"].(string)
		var res any
		switch {
		case strings.HasPrefix(expr, "/*mono:probe*/"):
			why := ""
			if fp.Submit {
				why = "type=submit"
			}
			res = map[string]any{"found": true, "submit": fp.Submit, "why": why, "form": fp.form()}
		case strings.HasPrefix(expr, "/*mono:hold*/"):
			res = map[string]any{"found": true, "form": fp.form(), "digest": fp.digest()}
		case strings.HasPrefix(expr, "/*mono:trigger*/"):
			m := expectRE.FindStringSubmatch(expr)
			if m != nil && m[1] != "" && m[1] != fp.digest() {
				res = map[string]any{"changed": true}
			} else if sub := subRE.FindStringSubmatch(expr); sub == nil || sub[1] == `""` {
				fp.Triggered++
				res = map[string]any{"ok": true}
			} else if fp.Disabled {
				res = map[string]any{"disabled": true}
			} else if fp.Covered {
				res = map[string]any{"covered": true}
			} else {
				fp.Triggered++
				res = map[string]any{"ok": true, "click": true, "x": 120, "y": 48}
			}
		case strings.HasPrefix(expr, "/*mono:recheck*/"):
			switch {
			case fp.Drifts:
				res = map[string]any{"changed": true}
			case fp.MovesAway:
				res = map[string]any{"covered": true}
			default:
				res = map[string]any{"ok": true}
			}
		case strings.HasPrefix(expr, "/*mono:peek*/"), strings.HasPrefix(expr, "/*mono:collect*/"):
			if strings.HasPrefix(expr, "/*mono:peek*/") && fp.OnRead != nil {
				fp.OnRead()
			}
			if fp.ReadFails != "" && strings.HasPrefix(expr, "/*mono:peek*/") {
				return nil, errors.New(fp.ReadFails)
			}
			res = map[string]any{
				"fired": !fp.Blocks && !fp.Misdirects, "blocked": fp.Blocks,
				"misdirected": fp.Misdirects || fp.StrayAndFired, "changed": !fp.NoReaction,
			}
		}
		return map[string]any{"result": res}, nil
	}
}
