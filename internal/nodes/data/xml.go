package data

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// XMLNode parses or generates XML from item fields.
// Type: "data.xml"
type XMLNode struct{}

func (n *XMLNode) Type() string { return "data.xml" }

// PublishesToThirdParty: data.xml sends nothing off this machine (issue #1418).
func (XMLNode) PublishesToThirdParty() bool { return false }

func (n *XMLNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	operation, _ := config["operation"].(string)
	if operation == "xml_to_json" || operation == "json_to_xml" {
		return executeN8nXML(operation, input, config)
	}
	field, _ := config["field"].(string)
	outputField, _ := config["output_field"].(string)
	rootElement, _ := config["root_element"].(string)

	if outputField == "" {
		outputField = field
	}
	if rootElement == "" {
		rootElement = "root"
	}

	outItems := make([]workflow.Item, 0, len(input.Items))

	for _, item := range input.Items {
		newJSON := copyMap(item.JSON)

		switch operation {
		case "parse":
			xmlStr, _ := newJSON[field].(string)
			parsed, err := xmlToMap(strings.NewReader(xmlStr))
			if err != nil {
				return nil, fmt.Errorf("data.xml parse: %w", err)
			}
			newJSON[outputField] = parsed

		case "generate":
			xmlStr, err := mapToXML(rootElement, newJSON)
			if err != nil {
				return nil, fmt.Errorf("data.xml generate: %w", err)
			}
			newJSON[outputField] = xmlStr

		default:
			return nil, fmt.Errorf("data.xml: unknown operation %q", operation)
		}

		outItems = append(outItems, workflow.Item{JSON: newJSON, Binary: item.Binary})
	}

	return []workflow.NodeOutput{{Handle: "main", Items: outItems}}, nil
}

// xmlToMap converts an XML reader into a nested map[string]interface{}.
// Text nodes are stored under the key "#text". Attributes use "@attrName".
func xmlToMap(r io.Reader) (map[string]interface{}, error) {
	decoder := xml.NewDecoder(r)
	var stack []map[string]interface{}
	var nameStack []string
	var root map[string]interface{}

	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			node := make(map[string]interface{})
			for _, attr := range t.Attr {
				node["@"+attr.Name.Local] = attr.Value
			}
			stack = append(stack, node)
			nameStack = append(nameStack, t.Name.Local)

		case xml.EndElement:
			if len(stack) == 0 {
				break
			}
			node := stack[len(stack)-1]
			name := nameStack[len(nameStack)-1]
			stack = stack[:len(stack)-1]
			nameStack = nameStack[:len(nameStack)-1]

			if len(stack) == 0 {
				root = map[string]interface{}{name: node}
			} else {
				parent := stack[len(stack)-1]
				if existing, ok := parent[name]; ok {
					// Multiple children with same name → slice
					switch ev := existing.(type) {
					case []interface{}:
						parent[name] = append(ev, node)
					default:
						parent[name] = []interface{}{ev, node}
					}
				} else {
					parent[name] = node
				}
			}

		case xml.CharData:
			text := strings.TrimSpace(string(t))
			if text != "" && len(stack) > 0 {
				node := stack[len(stack)-1]
				if existing, ok := node["#text"]; ok {
					if s, ok := existing.(string); ok {
						node["#text"] = s + text
					} else {
						node["#text"] = text
					}
				} else {
					node["#text"] = text
				}
			}
		}
	}

	if root == nil {
		return make(map[string]interface{}), nil
	}
	return root, nil
}

// mapToXML converts a map into an XML string wrapped in rootElement.
func mapToXML(rootElement string, data map[string]interface{}) (string, error) {
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	if err := encodeMapToXML(&buf, rootElement, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func encodeMapToXML(buf *bytes.Buffer, name string, value interface{}) error {
	// Sanitize element name: replace spaces with underscores
	safeName := strings.ReplaceAll(name, " ", "_")

	switch v := value.(type) {
	case map[string]interface{}:
		buf.WriteString("<" + safeName + ">")
		for k, child := range v {
			if err := encodeMapToXML(buf, k, child); err != nil {
				return err
			}
		}
		buf.WriteString("</" + safeName + ">")

	case []interface{}:
		for _, elem := range v {
			if err := encodeMapToXML(buf, safeName, elem); err != nil {
				return err
			}
		}

	case nil:
		buf.WriteString("<" + safeName + "/>")

	default:
		buf.WriteString("<" + safeName + ">")
		escaped, err := xmlEscape(fmt.Sprintf("%v", v))
		if err != nil {
			return err
		}
		buf.WriteString(escaped)
		buf.WriteString("</" + safeName + ">")
	}
	return nil
}

func xmlEscape(s string) (string, error) {
	var buf bytes.Buffer
	if err := xml.EscapeText(&buf, []byte(s)); err != nil {
		return "", err
	}
	return buf.String(), nil
}
