package data

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/monoes/mono-agent/internal/workflow"
)

// HTMLNode performs HTML extraction and generation operations.
// Type: "data.html"
type HTMLNode struct{}

func (n *HTMLNode) Type() string { return "data.html" }

// PublishesToThirdParty: data.html sends nothing off this machine (issue #1418).
func (HTMLNode) PublishesToThirdParty() bool { return false }

// PerItemConfigFields holds back the generate_html_template template and
// the convert_to_html_table row/cell attributes so they are evaluated
// against each item rather than only the first.
func (n *HTMLNode) PerItemConfigFields() []string { return htmlPerItemFields }

func (n *HTMLNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	operation, _ := config["operation"].(string)
	switch operation {
	case "extract_html_content":
		return executeExtractHTMLContent(input, config)
	case "generate_html_template":
		return executeGenerateHTMLTemplate(input, config)
	case "convert_to_html_table":
		return executeConvertToHTMLTable(input, config)
	}
	field, _ := config["field"].(string)
	selector, _ := config["selector"].(string)
	attribute, _ := config["attribute"].(string)
	outputField, _ := config["output_field"].(string)
	tmplStr, _ := config["template"].(string)

	if outputField == "" {
		outputField = field
	}

	outItems := make([]workflow.Item, 0, len(input.Items))

	for _, item := range input.Items {
		newJSON := copyMap(item.JSON)

		switch operation {
		case "extract":
			htmlStr, _ := newJSON[field].(string)
			doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlStr))
			if err != nil {
				return nil, fmt.Errorf("data.html extract: parse HTML: %w", err)
			}
			sel := doc.Find(selector).First()
			if attribute != "" {
				val, _ := sel.Attr(attribute)
				newJSON[outputField] = val
			} else {
				newJSON[outputField] = sel.Text()
			}

		case "extract_all":
			htmlStr, _ := newJSON[field].(string)
			doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlStr))
			if err != nil {
				return nil, fmt.Errorf("data.html extract_all: parse HTML: %w", err)
			}
			results := make([]string, 0)
			doc.Find(selector).Each(func(_ int, s *goquery.Selection) {
				if attribute != "" {
					val, _ := s.Attr(attribute)
					results = append(results, val)
				} else {
					results = append(results, s.Text())
				}
			})
			// Store as []interface{} for JSON compatibility
			iface := make([]interface{}, len(results))
			for i, r := range results {
				iface[i] = r
			}
			newJSON[outputField] = iface

		case "text":
			htmlStr, _ := newJSON[field].(string)
			doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlStr))
			if err != nil {
				return nil, fmt.Errorf("data.html text: parse HTML: %w", err)
			}
			newJSON[outputField] = doc.Text()

		case "generate":
			tmpl, err := template.New("html").Parse(tmplStr)
			if err != nil {
				return nil, fmt.Errorf("data.html generate: parse template: %w", err)
			}
			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, newJSON); err != nil {
				return nil, fmt.Errorf("data.html generate: execute template: %w", err)
			}
			newJSON[outputField] = buf.String()

		default:
			return nil, fmt.Errorf("data.html: unknown operation %q", operation)
		}

		outItems = append(outItems, workflow.Item{JSON: newJSON, Binary: item.Binary})
	}

	return []workflow.NodeOutput{{Handle: "main", Items: outItems}}, nil
}
