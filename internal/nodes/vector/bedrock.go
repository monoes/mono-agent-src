package vector

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// bedrockNow is swapped by tests for a stable signature.
var bedrockNow = func() time.Time { return time.Now().UTC() }

// embedBedrock invokes an AWS Bedrock embedding model. Amazon Titan models
// take one text per call; Cohere models on Bedrock take a batch.
func (c embedConfig) embedBedrock(ctx context.Context, texts []string, p purpose) ([][]float64, error) {
	if strings.HasPrefix(c.Model, "cohere.") {
		inputType := c.InputType
		if inputType == "" {
			inputType = "search_document"
			if p == purposeQuery {
				inputType = "search_query"
			}
		}
		var resp struct {
			Embeddings [][]float64 `json:"embeddings"`
		}
		if err := c.bedrockInvoke(ctx, map[string]interface{}{"texts": texts, "input_type": inputType}, &resp); err != nil {
			return nil, err
		}
		return resp.Embeddings, nil
	}
	out := make([][]float64, 0, len(texts))
	for _, t := range texts {
		body := map[string]interface{}{"inputText": t}
		if c.Dimensions > 0 {
			body["dimensions"] = c.Dimensions
		}
		var resp struct {
			Embedding []float64 `json:"embedding"`
		}
		if err := c.bedrockInvoke(ctx, body, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Embedding)
	}
	return out, nil
}

// bedrockInvoke POSTs to /model/{id}/invoke with an AWS Signature V4.
func (c embedConfig) bedrockInvoke(ctx context.Context, body interface{}, out interface{}) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	// Model ids carry ':' (amazon.titan-embed-text-v2:0); AWS expects it
	// percent-encoded in the request path.
	endpoint := c.BaseURL + "/model/" + strings.ReplaceAll(url.PathEscape(c.Model), ":", "%3A") + "/invoke"
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	signV4(req, payload, c.AccessKeyID, c.SecretAccessKey, c.SessionToken, c.Region, "bedrock", bedrockNow())
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &httpStatusError{Status: resp.StatusCode, Body: errorBodyEcho(data)}
	}
	return json.Unmarshal(data, out)
}

// signV4 adds AWS Signature Version 4 headers to req.
func signV4(req *http.Request, payload []byte, accessKey, secretKey, sessionToken, region, service string, now time.Time) {
	amzDate := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	payloadHash := sha256Hex(payload)

	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if sessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", sessionToken)
	}
	host := req.URL.Host
	signed := []string{"content-type", "host", "x-amz-content-sha256", "x-amz-date"}
	if sessionToken != "" {
		signed = append(signed, "x-amz-security-token")
	}
	var canonHeaders strings.Builder
	for _, h := range signed {
		v := req.Header.Get(h)
		if h == "host" {
			v = host
		}
		canonHeaders.WriteString(h + ":" + strings.TrimSpace(v) + "\n")
	}
	signedHeaders := strings.Join(signed, ";")
	canonPath := req.URL.EscapedPath()
	if canonPath == "" {
		canonPath = "/"
	}
	// SigV4 for non-S3 services URI-encodes each path segment twice.
	canonPath = strings.ReplaceAll(url.PathEscape(canonPath), "%2F", "/")
	canonical := strings.Join([]string{
		req.Method, canonPath, req.URL.RawQuery, canonHeaders.String(), signedHeaders, payloadHash,
	}, "\n")
	scope := day + "/" + region + "/" + service + "/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonical))

	key := hmacSHA256([]byte("AWS4"+secretKey), day)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))

	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, scope, signedHeaders, sig))
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}
