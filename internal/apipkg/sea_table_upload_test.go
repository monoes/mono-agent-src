package apipkg

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// SeaTable asset.upload: the upload link and upload use the API token,
// the row read and update the base access token; the new asset is
// appended to the column's existing ones.
func TestSeaTableAssetUpload(t *testing.T) {
	p := catalogPkg(t, "sea_table")
	for _, kind := range []string{"image", "file"} {
		var auths []string
		var update map[string]any
		pagedServer(t, func(_ int, r *http.Request, w http.ResponseWriter) {
			auths = append(auths, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
			switch r.URL.Path {
			case "/api/v2.1/dtable/app-upload-link/":
				fmt.Fprint(w, `{"upload_link":"https://cloud.seatable.io/seafhttp/upload-api/TOK","parent_path":"/asset/U1","img_relative_path":"images/x","file_relative_path":"files/x"}`)
			case "/api/v2.1/dtable/app-access-token/":
				fmt.Fprint(w, `{"access_token":"AT","dtable_uuid":"U1","workspace_id":7}`)
			case "/api-gateway/api/v2/dtables/U1/rows/r1":
				fmt.Fprint(w, `{"Col":["old"]}`)
			case "/seafhttp/upload-api/TOK":
				fmt.Fprint(w, `[{"name":"a.png","size":2}]`)
			case "/api-gateway/api/v2/dtables/U1/rows/":
				b, _ := io.ReadAll(r.Body)
				json.Unmarshal(b, &update)
				fmt.Fprint(w, `{"success":true}`)
			}
		})
		cfg := map[string]any{"resource": "asset", "operation": "upload", "tableName": "T", "uploadColumn": "Col:::" + kind,
			"rowId": "r1", "credential": map[string]any{"token": "API"}}
		in := workflow.NodeInput{Items: []workflow.Item{{JSON: map[string]any{}, Binary: map[string][]byte{"data": []byte("hi")}}}}
		out, err := (&Node{pkg: p}).Execute(t.Context(), in, cfg)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if items := out[0].Items; len(items) != 1 || items[0].JSON["name"] != "a.png" {
			t.Errorf("%s items %v", kind, items)
		}
		wantAuth := []string{
			"GET /api/v2.1/dtable/app-upload-link/ Token API",
			"GET /api/v2.1/dtable/app-access-token/ Token API",
			"GET /api-gateway/api/v2/dtables/U1/rows/r1 Token AT",
			"POST /seafhttp/upload-api/TOK Token API",
			"PUT /api-gateway/api/v2/dtables/U1/rows/ Token AT",
		}
		if !reflect.DeepEqual(auths, wantAuth) {
			t.Errorf("%s requests %v", kind, auths)
		}
		url := "https://cloud.seatable.io/workspace/7/asset/U1/" + kind + "s/x/a.png"
		var newAsset any = url
		if kind == "file" {
			newAsset = map[string]any{"name": "a.png", "size": 2.0, "type": "file", "url": url}
		}
		want := map[string]any{"table_name": "T", "updates": []any{map[string]any{"row_id": "r1",
			"row": map[string]any{"Col": []any{"old", newAsset}}}}}
		if !reflect.DeepEqual(update, want) {
			t.Errorf("%s update %v", kind, update)
		}
	}
}
