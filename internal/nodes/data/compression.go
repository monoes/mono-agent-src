package data

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"io"

	"github.com/monoes/mono-agent/internal/workflow"
)

// CompressionNode compresses or decompresses data using gzip or zip.
// Input/output values in item fields are base64-encoded strings.
// Type: "data.compression"
type CompressionNode struct{}

func (n *CompressionNode) Type() string { return "data.compression" }

// PublishesToThirdParty: data.compression sends nothing off this machine (issue #1418).
func (CompressionNode) PublishesToThirdParty() bool { return false }

func (n *CompressionNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	operation, _ := config["operation"].(string)
	field, _ := config["input_field"].(string)
	outputField, _ := config["output_field"].(string)
	filename, _ := config["filename"].(string)

	if operation == "compress" || operation == "decompress" {
		return n.executeFiles(input, config, operation)
	}

	if outputField == "" {
		outputField = field
	}
	if filename == "" {
		filename = "data"
	}

	outItems := make([]workflow.Item, 0, len(input.Items))

	for _, item := range input.Items {
		newJSON := copyMap(item.JSON)

		fieldVal, _ := newJSON[field].(string)

		var result string
		var err error

		switch operation {
		case "gzip":
			// fieldVal is a base64-encoded raw input
			raw, decErr := base64.StdEncoding.DecodeString(fieldVal)
			if decErr != nil {
				// treat as plain string if not valid base64
				raw = []byte(fieldVal)
			}
			result, err = gzipCompress(raw)

		case "gunzip":
			raw, decErr := base64.StdEncoding.DecodeString(fieldVal)
			if decErr != nil {
				return nil, fmt.Errorf("data.compression gunzip: base64 decode: %w", decErr)
			}
			result, err = gzipDecompress(raw)

		case "zip":
			raw, decErr := base64.StdEncoding.DecodeString(fieldVal)
			if decErr != nil {
				raw = []byte(fieldVal)
			}
			result, err = zipCompress(filename, raw)

		case "unzip":
			raw, decErr := base64.StdEncoding.DecodeString(fieldVal)
			if decErr != nil {
				return nil, fmt.Errorf("data.compression unzip: base64 decode: %w", decErr)
			}
			result, err = zipDecompress(raw)

		default:
			return nil, fmt.Errorf("data.compression: unknown operation %q", operation)
		}

		if err != nil {
			return nil, fmt.Errorf("data.compression %s: %w", operation, err)
		}

		newJSON[outputField] = result
		outItems = append(outItems, workflow.Item{JSON: newJSON, Binary: item.Binary})
	}

	return []workflow.NodeOutput{{Handle: "main", Items: outItems}}, nil
}

func gzipCompress(data []byte) (string, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

func gzipDecompress(data []byte) (string, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	defer r.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

func zipCompress(filename string, data []byte) (string, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create(filename)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

func zipDecompress(data []byte) (string, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	if len(r.File) == 0 {
		return base64.StdEncoding.EncodeToString([]byte{}), nil
	}
	// Read first file in the archive
	rc, err := r.File[0].Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	out, err := io.ReadAll(rc)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(out), nil
}
