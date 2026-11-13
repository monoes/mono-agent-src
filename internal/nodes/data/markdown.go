package data

import (
	"bytes"
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

// MarkdownNode converts Markdown text to HTML.
// Type: "data.markdown"
type MarkdownNode struct{}

func (n *MarkdownNode) Type() string { return "data.markdown" }

// PublishesToThirdParty: data.markdown sends nothing off this machine (issue #1418).
func (MarkdownNode) PublishesToThirdParty() bool { return false }

// PerItemConfigFields holds back the markdown/html source values and the
// destination key so they are evaluated against each item.
func (n *MarkdownNode) PerItemConfigFields() []string { return markdownPerItemFields }

func (n *MarkdownNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	switch mode, _ := config["mode"].(string); mode {
	case "":
	case "markdown_to_html", "html_to_markdown":
		return executeN8nMarkdown(mode, input, config)
	default:
		return nil, fmt.Errorf("%w: data.markdown: unknown mode %q (want markdown_to_html or html_to_markdown)", workflow.ErrInvalidConfig, mode)
	}
	field, _ := config["field"].(string)
	outputField, _ := config["output_field"].(string)
	unsafe, _ := config["unsafe"].(bool)

	if outputField == "" {
		outputField = field
	}

	opts := []goldmark.Option{
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
		),
	}
	if unsafe {
		opts = append(opts, goldmark.WithRendererOptions(html.WithUnsafe()))
	}
	md := goldmark.New(opts...)

	outItems := make([]workflow.Item, 0, len(input.Items))

	for _, item := range input.Items {
		newJSON := copyMap(item.JSON)

		src, _ := newJSON[field].(string)
		var buf bytes.Buffer
		if err := md.Convert([]byte(src), &buf); err != nil {
			return nil, fmt.Errorf("data.markdown: convert field %q: %w", field, err)
		}
		newJSON[outputField] = buf.String()

		outItems = append(outItems, workflow.Item{JSON: newJSON, Binary: item.Binary})
	}

	return []workflow.NodeOutput{{Handle: "main", Items: outItems}}, nil
}
