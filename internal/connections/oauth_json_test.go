package connections

import (
	"encoding/json"
	"testing"
)

// Platform definitions are printed as JSON (connect list --json), so the
// OAuth hook funcs must stay out of the encoding.
func TestPlatformDefsMarshalWithOAuthHooks(t *testing.T) {
	p := PlatformDef{ID: "tenant_x", OAuth: &OAuthConfig{
		Tailor:     func(c OAuthConfig, _ map[string]interface{}) (OAuthConfig, error) { return c, nil },
		AfterToken: nil,
	}}
	if _, err := json.Marshal(append(All(), p)); err != nil {
		t.Fatalf("marshal platforms: %v", err)
	}
}
