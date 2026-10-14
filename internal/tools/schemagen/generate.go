package schemagen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// marshalNoEscape renders v as JSON without HTML-escaping '<', '>', and '&'
// (encoding/json.Marshal's default behavior, meant for embedding JSON in
// HTML, would otherwise turn a template placeholder like
// "{{ $json.age > 18 }}" into "{{ $json.age > 18 }}"). prefix/indent
// mirror json.Encoder.SetIndent's params; pass "", "" for compact encoding.
func marshalNoEscape(v interface{}, prefix, indent string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent(prefix, indent)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// Encode always appends a trailing newline; the caller controls newlines.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// generatedMarker is the sentinel key written into every generator-produced
// schema file. It is not part of workflow.NodeSchema — schema_loader.go's
// json.Unmarshal silently ignores unknown fields, so this adds zero risk to
// the loader — but it is the single documented signal (see
// internal/workflow/schemas/README.md) that a schema file is generated and
// must not be hand-edited.
const generatedMarker = "_generated"

// RepoRoot walks up from the current working directory looking for go.mod.
func RepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("schemagen: go.mod not found above %s", dir)
		}
		dir = parent
	}
}

// RenderJSON generates the schema for entry and renders it as indented JSON
// with the generated marker, matching the style of the hand-written schema
// files (2-space indent, trailing newline).
func RenderJSON(repoRoot string, entry ManifestEntry) ([]byte, error) {
	schema, err := GenerateSchema(filepath.Join(repoRoot, entry.GoFile), entry.StructName)
	if err != nil {
		return nil, err
	}

	// Round-trip through map[string]interface{} so the marker key can be
	// injected as a real (first) key rather than a bolted-on wrapper type.
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("schemagen: marshal %s: %w", entry.NodeType, err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("schemagen: re-decode %s: %w", entry.NodeType, err)
	}
	m[generatedMarker] = true
	// Outputs and AI sub-node connections aren't derived from the struct
	// tags: keep the hand-written "outputs", "sub_inputs" and "sub_output"
	// of the committed file.
	if existing, err := os.ReadFile(SchemaPath(repoRoot, entry)); err == nil {
		var prev map[string]interface{}
		if json.Unmarshal(existing, &prev) == nil {
			for _, k := range []string{"outputs", "sub_inputs", "sub_output"} {
				if v, ok := prev[k]; ok {
					m[k] = v
				}
			}
		}
	}

	out, err := marshalOrdered(m)
	if err != nil {
		return nil, fmt.Errorf("schemagen: encode %s: %w", entry.NodeType, err)
	}
	return append(out, '\n'), nil
}

// SchemaPath returns the output path for entry's generated schema, relative
// to repoRoot.
func SchemaPath(repoRoot string, entry ManifestEntry) string {
	return filepath.Join(repoRoot, "internal", "workflow", "schemas", entry.NodeType+".json")
}

// marshalOrdered renders m as indented JSON with a stable, readable key
// order: _generated and credential_platform first, fields last, matching
// where hand-written schema files put them.
func marshalOrdered(m map[string]interface{}) ([]byte, error) {
	order := []string{generatedMarker, "credential_platform", "fields"}
	seen := make(map[string]bool, len(order))

	var b []byte
	b = append(b, '{', '\n')
	first := true
	writeKey := func(k string) error {
		v, ok := m[k]
		if !ok {
			return nil
		}
		seen[k] = true
		if !first {
			b = append(b, ',', '\n')
		}
		first = false
		keyJSON, err := marshalNoEscape(k, "", "")
		if err != nil {
			return err
		}
		valJSON, err := marshalNoEscape(v, "  ", "  ")
		if err != nil {
			return err
		}
		b = append(b, "  "...)
		b = append(b, keyJSON...)
		b = append(b, ": "...)
		b = append(b, valJSON...)
		return nil
	}
	for _, k := range order {
		if err := writeKey(k); err != nil {
			return nil, err
		}
	}
	rest := make([]string, 0, len(m))
	for k := range m {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		if err := writeKey(k); err != nil {
			return nil, err
		}
	}
	b = append(b, '\n', '}')
	return b, nil
}
