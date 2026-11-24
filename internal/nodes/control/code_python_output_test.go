package control

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

func TestCappedBufferRefusesPastItsLimit(t *testing.T) {
	b := &cappedBuffer{max: 10}
	if _, err := b.Write([]byte("12345")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("678901")); err == nil || !b.exceeded {
		t.Fatalf("a write past the limit was accepted (err=%v exceeded=%v)", err, b.exceeded)
	}
	if b.String() != "12345" {
		t.Errorf("buffer = %q", b.String())
	}
}

func TestCodePython_UnboundedOutputIsRefused(t *testing.T) {
	needPython(t)
	code := "import sys\nfor _ in range(80):\n  sys.stderr.write('x' * 1000000)\nreturn []"
	_, err := runPy(t, workflow.NodeInput{Items: pyItems(1)}, code, map[string]interface{}{"timeout_seconds": float64(60)})
	if err == nil || !strings.Contains(err.Error(), "more than 64 MB") {
		t.Fatalf("err = %v", err)
	}
}
