package data

import (
	"context"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"

	"github.com/google/uuid"
	"github.com/monoes/mono-agent/internal/workflow"
)

// CryptoNode performs cryptographic and encoding operations.
// Type: "data.crypto"
type CryptoNode struct{}

func (n *CryptoNode) Type() string { return "data.crypto" }

// PublishesToThirdParty: data.crypto sends nothing off this machine (issue #1418).
func (CryptoNode) PublishesToThirdParty() bool { return false }

func (n *CryptoNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	operation, _ := config["operation"].(string)
	if cryptoActions[operation] {
		return n.executeAction(input, config, operation)
	}
	field, _ := config["field"].(string)
	key, _ := config["secret"].(string)
	if key == "" {
		key, _ = config["key"].(string)
	}
	outputField, _ := config["output_field"].(string)
	encoding, _ := config["encoding"].(string)

	length := 16
	if l, ok := config["length"].(float64); ok {
		length = int(l)
	}

	if encoding == "" {
		encoding = "hex"
	}
	if outputField == "" {
		outputField = field
	}

	outItems := make([]workflow.Item, 0, len(input.Items))

	for _, item := range input.Items {
		newJSON := copyMap(item.JSON)

		switch operation {
		case "hash_md5":
			data := []byte(fmt.Sprintf("%v", newJSON[field]))
			h := md5.Sum(data) //nolint:gosec
			newJSON[outputField] = encodeHash(h[:], encoding)

		case "hash_sha256":
			data := []byte(fmt.Sprintf("%v", newJSON[field]))
			h := sha256.Sum256(data)
			newJSON[outputField] = encodeHash(h[:], encoding)

		case "hash_sha512":
			data := []byte(fmt.Sprintf("%v", newJSON[field]))
			h := sha512.Sum512(data)
			newJSON[outputField] = encodeHash(h[:], encoding)

		case "hmac_sha256":
			data := []byte(fmt.Sprintf("%v", newJSON[field]))
			var hm hash.Hash = hmac.New(sha256.New, []byte(key))
			hm.Write(data)
			newJSON[outputField] = encodeHash(hm.Sum(nil), encoding)

		case "uuid":
			newJSON[outputField] = uuid.New().String()

		case "random_bytes":
			buf := make([]byte, length)
			if _, err := rand.Read(buf); err != nil {
				return nil, fmt.Errorf("data.crypto random_bytes: %w", err)
			}
			newJSON[outputField] = base64.StdEncoding.EncodeToString(buf)

		case "base64_encode":
			data := fmt.Sprintf("%v", newJSON[field])
			newJSON[outputField] = base64.StdEncoding.EncodeToString([]byte(data))

		case "base64_decode":
			data, _ := newJSON[field].(string)
			decoded, err := base64.StdEncoding.DecodeString(data)
			if err != nil {
				return nil, fmt.Errorf("data.crypto base64_decode: field %q: %w", field, err)
			}
			newJSON[outputField] = string(decoded)

		default:
			return nil, fmt.Errorf("data.crypto: unknown operation %q", operation)
		}

		outItems = append(outItems, workflow.Item{JSON: newJSON, Binary: item.Binary})
	}

	return []workflow.NodeOutput{{Handle: "main", Items: outItems}}, nil
}

func encodeHash(b []byte, encoding string) string {
	if encoding == "base64" {
		return base64.StdEncoding.EncodeToString(b)
	}
	return hex.EncodeToString(b)
}
