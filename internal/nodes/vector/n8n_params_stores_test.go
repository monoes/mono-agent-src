package vector

import (
	"reflect"
	"testing"
)

func TestN8nAzureDatabricksOracleStoresMap(t *testing.T) {
	cases := []struct {
		typ    string
		params map[string]interface{}
		want   map[string]interface{}
	}{
		{"vectorStoreAzureAISearch", map[string]interface{}{"mode": "load", "indexName": "kb", "prompt": "q",
			"options": map[string]interface{}{"queryType": "semanticHybrid", "filter": "metadata/source eq 'a'", "semanticConfiguration": "sem"}},
			map[string]interface{}{"provider": "azure_ai_search", "mode": "search", "collection": "kb", "query": "q",
				"query_type": "semanticHybrid", "filter": "metadata/source eq 'a'", "semantic_configuration": "sem"}},
		{"vectorStoreDatabricks", map[string]interface{}{"mode": "insert", "contentColumn": "body",
			"databricksIndex": map[string]interface{}{"__rl": true, "mode": "id", "value": "main.rag.docs"}},
			map[string]interface{}{"provider": "databricks", "mode": "insert", "collection": "main.rag.docs", "content_column": "body"}},
		{"vectorStoreOracleDBVector", map[string]interface{}{"mode": "retrieve", "tableName": "n8n_vectors",
			"options": map[string]interface{}{"distanceStrategy": "DOT", "metadata": map[string]interface{}{
				"metadataValues": []interface{}{map[string]interface{}{"name": "lang", "value": "en"}}}}},
			map[string]interface{}{"provider": "oracle", "mode": "search", "collection": "n8n_vectors", "distance": "DOT", "id_column": "external_id",
				"filter": []interface{}{map[string]interface{}{"name": "lang", "value": "en"}}}},
	}
	for _, c := range cases {
		got, _, err := ConvertN8nParams(N8nPrefix+c.typ, c.params)
		if err != nil {
			t.Fatalf("%s: %v", c.typ, err)
		}
		if !reflect.DeepEqual(got.Config, c.want) {
			t.Errorf("%s:\n got %#v\nwant %#v", c.typ, got.Config, c.want)
		}
		if _, err := parseStoreConfig(got.Config); err != nil {
			t.Errorf("%s: %v", c.typ, err)
		}
	}
}
