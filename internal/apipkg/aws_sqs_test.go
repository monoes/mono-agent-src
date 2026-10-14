package apipkg

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// SQS binary message attributes carry the item's binary property base64,
// numbered between the string and number attributes as n8n does.
func TestSQSBinaryMessageAttributes(t *testing.T) {
	p := catalogPkg(t, "aws_sqs")
	var form url.Values
	pagedServer(t, func(_ int, r *http.Request, w http.ResponseWriter) {
		r.ParseForm()
		form = r.PostForm
		w.Write([]byte(`{"SendMessageResponse":{}}`))
	})
	cfg := map[string]any{"operation": "sendMessage", "queue": "https://sqs.us-east-1.amazonaws.com/123/jobs",
		"message": "hi", "credential": map[string]any{"access_key_id": "A", "secret_access_key": "S", "region": "us-east-1"},
		"options": map[string]any{"messageAttributes": map[string]any{
			"string": []any{map[string]any{"name": "kind", "value": "a"}},
			"binary": []any{map[string]any{"name": "blob", "dataPropertyName": "img"}},
			"number": []any{map[string]any{"name": "n", "value": 3}}}}}
	in := workflow.NodeInput{Items: []workflow.Item{{JSON: map[string]any{}, Binary: map[string][]byte{"img": []byte("hi!")}}}}
	if _, err := (&Node{pkg: p}).Execute(t.Context(), in, cfg); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"MessageAttribute.1.Name": "kind", "MessageAttribute.1.Value.DataType": "String",
		"MessageAttribute.2.Name": "blob", "MessageAttribute.2.Value.BinaryValue": "aGkh", "MessageAttribute.2.Value.DataType": "Binary",
		"MessageAttribute.3.Name": "n", "MessageAttribute.3.Value.StringValue": "3", "MessageAttribute.3.Value.DataType": "Number",
	}
	for k, v := range want {
		if got := form.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if form.Get("MessageAttribute.2.Value.StringValue") != "" {
		t.Error("binary attribute got a StringValue")
	}
}
