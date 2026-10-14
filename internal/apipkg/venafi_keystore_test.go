package apipkg

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"golang.org/x/crypto/nacl/box"
)

// A Venafi keystore download reads the certificate's dekHash, fetches that
// edge encryption key and sends the passphrases as sealed boxes the key's
// owner can open.
func TestVenafiKeystoreDownloadSealsPassphrases(t *testing.T) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p := catalogPkg(t, "venafi_tls_protect_cloud")
	var body map[string]any
	seen := pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		switch r.URL.Path {
		case "/outagedetection/v1/certificates/c1":
			fmt.Fprint(w, `{"id":"c1","dekHash":"h1"}`)
		case "/v1/edgeencryptionkeys/h1":
			fmt.Fprintf(w, `{"key":%q}`, base64.StdEncoding.EncodeToString(pub[:]))
		case "/outagedetection/v1/certificates/c1/keystore":
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &body)
			w.Header().Set("Content-Disposition", `attachment; filename="c1.jks"`)
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write([]byte("KS"))
		default:
			http.Error(w, r.URL.Path, http.StatusNotFound)
		}
	})
	params := map[string]any{"resource": "certificate", "operation": "download", "certificateId": "c1",
		"downloadItem": "keystore", "keystoreType": "JKS", "certificateLabel": "web",
		"privateKeyPassphrase": "pk-secret", "keystorePassphrase": "ks-secret", "binaryProperty": "data"}
	items, err := runPackageOp(p, params, map[string]any{"api_key": "k"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 3 || (*seen)[2].Method != http.MethodPost || len((*seen)[2].URL.RawQuery) != 0 {
		t.Fatalf("requests %d, last %s %s", len(*seen), (*seen)[2].Method, (*seen)[2].URL)
	}
	if string(items[0].Binary["data"]) != "KS" || items[0].JSON["file_name"] != "c1.jks" {
		t.Errorf("item %v", items[0].JSON)
	}
	if body["exportFormat"] != "JKS" || body["certificateLabel"] != "web" {
		t.Errorf("body %v", body)
	}
	for key, want := range map[string]string{"encryptedPrivateKeyPassphrase": "pk-secret", "encryptedKeystorePassphrase": "ks-secret"} {
		sealed, err := base64.StdEncoding.DecodeString(fmt.Sprint(body[key]))
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if got, ok := box.OpenAnonymous(nil, sealed, pub, priv); !ok || string(got) != want {
			t.Errorf("%s opens to %q (%v)", key, got, ok)
		}
	}

	// PKCS12: no keystore passphrase.
	params["keystoreType"] = "PKCS12"
	*seen, body = nil, nil
	if _, err := runPackageOp(p, params, map[string]any{"api_key": "k"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["encryptedKeystorePassphrase"]; ok || body["encryptedPrivateKeyPassphrase"] == nil {
		t.Errorf("PKCS12 body %v", body)
	}
	if _, err := renderString("{{ p.x | sealbox:'bm9wZQ==' }}", Scope{P: map[string]any{"x": "a"}}); err == nil {
		t.Error("a short key must fail")
	}
}
