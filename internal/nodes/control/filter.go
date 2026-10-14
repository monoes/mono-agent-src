package control

import (
	"context"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// FilterNode filters items by evaluating a boolean expression on each item.
// Config fields:
//
//	"condition" (string, required): boolean expression template
//	"mode" (string, optional): "keep" (default) = keep items where condition is true,
//	                           "remove" = keep items where condition is false
//
// Items that pass the condition are emitted on "main".
// Items that do not pass are emitted on "rejected".
type FilterNode struct{}

func (n *FilterNode) Type() string { return "core.filter" }

// PerItemConfigFields declares that "condition" must not be pre-resolved
// using only the first input item — it's evaluated fresh for every item below.
func (n *FilterNode) PerItemConfigFields() []string { return []string{"condition"} }

func (n *FilterNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	condition, _ := config["condition"].(string)
	if condition == "" {
		return nil, fmt.Errorf("%w: filter node requires \"condition\"", workflow.ErrInvalidConfig)
	}

	mode, _ := config["mode"].(string)
	if mode == "" {
		mode = "keep"
	}
	if mode != "keep" && mode != "remove" {
		return nil, fmt.Errorf("%w: filter node \"mode\" must be \"keep\" or \"remove\"", workflow.ErrInvalidConfig)
	}

	engine := workflow.NewExpressionEngine()

	passed := make([]workflow.Item, 0)
	rejected := make([]workflow.Item, 0)

	for _, item := range input.Items {
		exprCtx := input.ItemContext(item.JSON)

		result, err := engine.EvaluateBool(condition, exprCtx)
		if err != nil {
			// A condition that cannot be parsed or evaluated must fail the
			// node. The previous behavior treated every error as a clean
			// false, so a typo'd condition silently dropped all items and
			// still reported the execution as SUCCESS. EvaluateBool's
			// conversion error embeds the evaluated value; never echo item
			// data back in the error message.
			msg := err.Error()
			if strings.Contains(msg, "cannot convert") {
				msg = "expression did not evaluate to a boolean (expected true/false, 1/0, or yes/no)"
			}
			return nil, fmt.Errorf("filter condition error: %s", msg)
		}

		// Decide whether to keep this item based on mode.
		keep := (mode == "keep" && result) || (mode == "remove" && !result)
		if keep {
			passed = append(passed, item)
		} else {
			rejected = append(rejected, item)
		}
	}

	return []workflow.NodeOutput{
		{Handle: "main", Items: passed},
		{Handle: "rejected", Items: rejected},
	}, nil
}
