package action

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"testing"
)

func TestPackageUploadConfinement(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	uploads := filepath.Join(home, ".monoagent", "uploads", "imp")
	if err := os.MkdirAll(uploads, 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(uploads, "photo.png")
	secret := filepath.Join(home, ".ssh", "id_rsa")
	userFile := filepath.Join(home, "Pictures", "me.png")
	for _, f := range []string{inside, secret, userFile} {
		_ = os.MkdirAll(filepath.Dir(f), 0o755)
		_ = os.WriteFile(f, []byte("x"), 0o600)
	}
	link := filepath.Join(uploads, "escape.png")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}

	newAE := func(vars map[string]interface{}, def string) *ActionExecutor {
		ae := newPkgExecutor(nil, &fakePkg{id: "imp"})
		ae.actionDef = &ActionDef{Inputs: &InputDef{Optional: []json.RawMessage{
			json.RawMessage(`{"name":"photo","type":"file","default":` + def + `}`),
			json.RawMessage(`{"name":"note","type":"string"}`),
		}}}
		for k, v := range vars {
			ae.SetVariable(k, v)
		}
		return ae
	}
	cases := []struct {
		name  string
		vars  map[string]interface{}
		def   string
		paths []string
		ok    bool
	}{
		{"inside uploads dir", nil, `""`, []string{inside}, true},
		{"ssh key named by the package", nil, `""`, []string{secret}, false},
		{"symlink out of uploads", nil, `""`, []string{link}, false},
		{"user-supplied file input", map[string]interface{}{"photo": userFile}, `""`, []string{userFile}, true},
		{"plain string input is not a file input", map[string]interface{}{"note": secret}, `""`, []string{secret}, false},
		{"file input still at the package default", map[string]interface{}{"photo": secret}, `"` + secret + `"`, []string{secret}, false},
		{"one bad path refuses all", map[string]interface{}{"photo": userFile}, `""`, []string{userFile, secret}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := newAE(c.vars, c.def).confineUploads(context.Background(), c.paths, c.paths)
			if (err == nil) != c.ok || (err != nil && !errors.Is(err, ErrUploadNotAllowed)) {
				t.Fatalf("err = %v, want ok=%v", err, c.ok)
			}
		})
	}

	// The step refuses before touching the page (this executor has none).
	ae := newAE(nil, `""`)
	res, err := ae.stepUpload(context.Background(), StepDef{ID: "u", Type: "upload", Text: secret})
	if err != nil || res.Success || !errors.Is(res.Error, ErrUploadNotAllowed) {
		t.Fatalf("stepUpload: res=%+v err=%v", res, err)
	}
	// No package: unchanged legacy behaviour (unconfined).
	legacy := newPkgExecutor(nil, nil)
	if err := legacy.confineUploads(context.Background(), []string{secret}, []string{secret}); err != nil {
		t.Fatalf("legacy: %v", err)
	}
}

func TestUploadConfinementExemptsBuiltinAndLocal(t *testing.T) {
	for _, tier := range []string{"builtin", "local"} {
		p := &coreTrustPkg{fakePkg: fakePkg{id: "tiktok"}, trust: tier}
		ae := newPkgExecutor(nil, p)
		if err := ae.confineUploads(context.Background(), []string{"/home/u/video.mp4"}, []string{"/home/u/video.mp4"}); err != nil {
			t.Errorf("%s: %v", tier, err)
		}
	}
	p := &coreTrustPkg{fakePkg: fakePkg{id: "rec"}, trust: "recorded"}
	if err := newPkgExecutor(nil, p).confineUploads(context.Background(), []string{"/etc/passwd"}, []string{"/etc/passwd"}); err == nil {
		t.Error("recorded package must be confined")
	}
}

// Safe mode stops before an upload, but a path the live run would refuse is
// reported as refused, not as a successful "would upload".
func TestSafeModeUploadPrecheck(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	uploads := filepath.Join(home, ".monoagent", "uploads", "rec")
	if err := os.MkdirAll(uploads, 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(uploads, "a.png")
	if err := os.WriteFile(inside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(path string) (*ActionExecutor, error) {
		ae := newPkgExecutor(nil, &coreTrustPkg{fakePkg: fakePkg{id: "rec"}, trust: "recorded"})
		ae.SetSafeMode(true)
		ae.SetVariable("file", path)
		def := &ActionDef{ActionType: "t", SideEffects: "write", Steps: []StepDef{
			{ID: "up", Type: "upload", Selector: "#f", Text: "{{file}}", SideEffect: true},
		}}
		_, err := ae.ExecuteDef(&StorageAction{ID: "a", TargetPlatform: "rec", Type: "t"}, def)
		return ae, err
	}

	ae, err := run("/etc/passwd")
	if !errors.Is(err, ErrRefused) || ae.SafeStopped() != nil {
		t.Fatalf("outside path: err=%v safeStop=%+v, want a refusal", err, ae.SafeStopped())
	}
	ae, err = run(inside)
	if err != nil || ae.SafeStopped() == nil || ae.SafeStopped().StepID != "up" {
		t.Fatalf("allowed path: err=%v safeStop=%+v, want a normal safe stop", err, ae.SafeStopped())
	}
}
