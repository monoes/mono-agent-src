package vector

import (
	"context"
	"fmt"
	"net/url"
	"sort"
)

// embedOpenAI covers OpenAI, OpenAI-compatible servers (OpenRouter, vLLM,
// LM Studio, llama.cpp …), Mistral and Azure OpenAI: POST {input:[…]} and
// read data[].embedding, ordered by data[].index.
func (c embedConfig) embedOpenAI(ctx context.Context, endpoint string, headers map[string]string, texts []string) ([][]float64, error) {
	body := map[string]interface{}{"input": texts, "encoding_format": "float"}
	if c.Provider != ProviderAzureOpenAI {
		body["model"] = c.Model
	}
	if c.Dimensions > 0 {
		body["dimensions"] = c.Dimensions
	}
	var resp struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := doJSON(ctx, "POST", endpoint, headers, body, &resp); err != nil {
		return nil, err
	}
	sort.SliceStable(resp.Data, func(i, j int) bool { return resp.Data[i].Index < resp.Data[j].Index })
	out := make([][]float64, len(resp.Data))
	for i, d := range resp.Data {
		out[i] = d.Embedding
	}
	return out, nil
}

// embedOllama uses Ollama's batch endpoint POST /api/embed.
func (c embedConfig) embedOllama(ctx context.Context, texts []string) ([][]float64, error) {
	body := map[string]interface{}{"model": c.Model, "input": texts}
	if c.Dimensions > 0 {
		body["dimensions"] = c.Dimensions
	}
	var resp struct {
		Embeddings [][]float64 `json:"embeddings"`
	}
	if err := doJSON(ctx, "POST", c.BaseURL+"/api/embed", map[string]string{"Authorization": bearer(c.APIKey)}, body, &resp); err != nil {
		return nil, err
	}
	return resp.Embeddings, nil
}

// embedCohere uses Cohere's v2 embed endpoint. input_type follows the
// purpose unless set explicitly.
func (c embedConfig) embedCohere(ctx context.Context, texts []string, p purpose) ([][]float64, error) {
	inputType := c.InputType
	if inputType == "" {
		inputType = "search_document"
		if p == purposeQuery {
			inputType = "search_query"
		}
	}
	body := map[string]interface{}{
		"model":           c.Model,
		"texts":           texts,
		"input_type":      inputType,
		"embedding_types": []string{"float"},
	}
	if c.Dimensions > 0 {
		body["output_dimension"] = c.Dimensions
	}
	var resp struct {
		Embeddings struct {
			Float [][]float64 `json:"float"`
		} `json:"embeddings"`
	}
	if err := doJSON(ctx, "POST", c.BaseURL+"/v2/embed", map[string]string{"Authorization": bearer(c.APIKey)}, body, &resp); err != nil {
		return nil, err
	}
	return resp.Embeddings.Float, nil
}

// embedGemini uses the Gemini API batchEmbedContents method.
func (c embedConfig) embedGemini(ctx context.Context, texts []string, p purpose) ([][]float64, error) {
	task := "RETRIEVAL_DOCUMENT"
	if p == purposeQuery {
		task = "RETRIEVAL_QUERY"
	}
	model := "models/" + c.Model
	reqs := make([]map[string]interface{}, len(texts))
	for i, t := range texts {
		r := map[string]interface{}{
			"model":    model,
			"content":  map[string]interface{}{"parts": []map[string]string{{"text": t}}},
			"taskType": task,
		}
		if c.Dimensions > 0 {
			r["outputDimensionality"] = c.Dimensions
		}
		reqs[i] = r
	}
	endpoint := fmt.Sprintf("%s/v1beta/%s:batchEmbedContents", c.BaseURL, model)
	var resp struct {
		Embeddings []struct {
			Values []float64 `json:"values"`
		} `json:"embeddings"`
	}
	if err := doJSON(ctx, "POST", endpoint, map[string]string{"x-goog-api-key": c.APIKey}, map[string]interface{}{"requests": reqs}, &resp); err != nil {
		return nil, err
	}
	out := make([][]float64, len(resp.Embeddings))
	for i, e := range resp.Embeddings {
		out[i] = e.Values
	}
	return out, nil
}

// embedHuggingFace calls the Inference feature-extraction pipeline.
// Sentence-transformer models return one vector per input; token-level
// models return one vector per token, which is mean-pooled here.
func (c embedConfig) embedHuggingFace(ctx context.Context, texts []string) ([][]float64, error) {
	endpoint := fmt.Sprintf("%s/models/%s/pipeline/feature-extraction", c.BaseURL, pathEscapeModel(c.Model))
	var resp []interface{}
	if err := doJSON(ctx, "POST", endpoint, map[string]string{"Authorization": bearer(c.APIKey)}, map[string]interface{}{"inputs": texts}, &resp); err != nil {
		return nil, err
	}
	out := make([][]float64, 0, len(resp))
	for i, row := range resp {
		if v, ok := toFloats(row); ok {
			out = append(out, v)
			continue
		}
		tokens, ok := row.([]interface{})
		if !ok || len(tokens) == 0 {
			return nil, fmt.Errorf("unexpected feature-extraction shape at input %d", i)
		}
		pooled, err := meanPool(tokens)
		if err != nil {
			return nil, fmt.Errorf("input %d: %w", i, err)
		}
		out = append(out, pooled)
	}
	return out, nil
}

func meanPool(tokens []interface{}) ([]float64, error) {
	var sum []float64
	for _, t := range tokens {
		v, ok := toFloats(t)
		if !ok {
			return nil, fmt.Errorf("unexpected token vector shape")
		}
		if sum == nil {
			sum = make([]float64, len(v))
		}
		if len(v) != len(sum) {
			return nil, fmt.Errorf("token vectors differ in length")
		}
		for i, f := range v {
			sum[i] += f
		}
	}
	for i := range sum {
		sum[i] /= float64(len(tokens))
	}
	return sum, nil
}

// pathEscapeModel escapes each segment of an "org/model" id.
func pathEscapeModel(m string) string {
	u := url.URL{Path: m}
	return u.EscapedPath()
}
