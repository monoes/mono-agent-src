package service

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/attachments"
	"github.com/monoes/mono-agent/internal/vault"
)

// R11-09: a profile's run keeps mail files in the profile's folder, and
// deleting the mail removes them in both layouts.
func TestOutlookAttachmentsLiveInTheProfileFolderAndGoWithTheMail(t *testing.T) {
	body := `{"value":[{"name":"a.txt","contentType":"text/plain","size":1,"contentBytes":"` +
		base64.StdEncoding.EncodeToString([]byte("x")) + `"}]}`
	orig := httpClient
	httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	t.Cleanup(func() { httpClient = orig })

	ctx := vault.ContextWithProfileID(context.Background(), "prof-1")
	files, err := downloadOutlookAttachments(ctx, "tok", "m-1")
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	path, _ := files[0]["path"].(string)
	if !strings.HasPrefix(path, attachments.ProfileDir("prof-1")+string(filepath.Separator)) {
		t.Fatalf("path %q is not under the profile folder %q", path, attachments.ProfileDir("prof-1"))
	}
	legacy, err := attachments.Save("m-1", "old.txt", []byte("y"))
	if err != nil {
		t.Fatal(err)
	}
	if err := removeOutlookAttachments(ctx, "m-1"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, legacy} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still there after the mail was deleted (%v)", p, err)
		}
	}
}
