package vector

import (
	"context"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// Splitter names accepted by vector.split_text.
const (
	SplitterCharacter = "character"
	SplitterRecursive = "recursive"
	SplitterToken     = "token"
)

// defaultRecursiveSeparators: paragraphs, lines, words, characters.
var defaultRecursiveSeparators = []string{"\n\n", "\n", " ", ""}

// SplitTextNode implements vector.split_text: each input item's text is cut
// into chunks, one output item per chunk, shaped like a document
// ({pageContent, metadata}) so vector.embed / vector.store take it as is.
type SplitTextNode struct{}

func (n *SplitTextNode) Type() string { return "vector.split_text" }

type splitConfig struct {
	splitter   string
	size       int
	overlap    int
	separator  string
	separators []string
	fields     []string
	metaFields []string
}

func parseSplitConfig(cfg map[string]interface{}) (splitConfig, error) {
	c := splitConfig{
		splitter:   strings.ToLower(strOr(cfg, SplitterRecursive, "splitter")),
		fields:     stringList(cfg, "input_field"),
		metaFields: stringList(cfg, "metadata_fields"),
	}
	var err error
	if c.size, err = intField(cfg, "chunk_size", 1000); err != nil {
		return c, err
	}
	if c.overlap, err = intField(cfg, "chunk_overlap", 0); err != nil {
		return c, err
	}
	if c.size == 0 {
		return c, fmt.Errorf("chunk_size must be at least 1")
	}
	if c.overlap >= c.size {
		return c, fmt.Errorf("chunk_overlap (%d) must be smaller than chunk_size (%d)", c.overlap, c.size)
	}
	switch c.splitter {
	case SplitterCharacter:
		c.separator = "\n\n"
		if raw, ok := cfg["separator"].(string); ok && raw != "" {
			c.separator = unescapeSeparator(raw)
		}
	case SplitterRecursive:
		c.separators = defaultRecursiveSeparators
		if raw, ok := cfg["separators"]; ok && raw != nil && raw != "" {
			seps, err := separatorList(raw)
			if err != nil {
				return c, err
			}
			c.separators = seps
		}
	case SplitterToken:
	default:
		return c, fmt.Errorf("splitter %q is not supported (want character, recursive or token)", c.splitter)
	}
	return c, nil
}

// separatorList accepts a JSON array of strings (where "" means "split
// into characters") or a comma-free newline-separated string.
func separatorList(raw interface{}) ([]string, error) {
	switch v := raw.(type) {
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("separators must be a list of strings, got %T", e)
			}
			out = append(out, unescapeSeparator(s))
		}
		return out, nil
	case string:
		var out []string
		for _, line := range strings.Split(v, "\n") {
			if line != "" {
				out = append(out, unescapeSeparator(line))
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("separators must be a list of strings, got %T", raw)
}

func (c splitConfig) split(text string) []string {
	switch c.splitter {
	case SplitterCharacter:
		return splitCharacter(text, c.separator, c.size, c.overlap)
	case SplitterToken:
		return splitTokens(text, c.size, c.overlap)
	}
	return splitRecursive(text, c.separators, c.size, c.overlap)
}

func (n *SplitTextNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	c, err := parseSplitConfig(config)
	if err != nil {
		return nil, fmt.Errorf("vector.split_text: %w", err)
	}
	var out []workflow.Item
	for i, item := range input.Items {
		text, err := itemText(item.JSON, c.fields)
		if err != nil {
			return nil, fmt.Errorf("vector.split_text: item %d: %w", i, err)
		}
		meta := documentMetadata(item.JSON, c.metaFields)
		chunks := c.split(text)
		for ci, chunk := range chunks {
			m := copyMap(meta)
			m["chunk_index"] = ci
			m["chunk_count"] = len(chunks)
			out = append(out, workflow.NewItem(map[string]interface{}{
				"pageContent": chunk,
				"metadata":    m,
			}))
		}
	}
	return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
}

// documentMetadata carries an item's existing "metadata" object forward
// and adds the listed item fields to it.
func documentMetadata(j map[string]interface{}, fields []string) map[string]interface{} {
	meta := copyMap(asMap(j["metadata"]))
	for _, f := range fields {
		if v, ok := lookupPath(j, f); ok {
			meta[f] = v
		}
	}
	return meta
}
