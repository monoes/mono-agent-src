package noderegistry

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A node type moved to EgressNone because its code was read and makes no
// network call of its own is pinned here: the files that make up the type
// import no package that dials out (and none that reaches a model provider or
// runs a program). The table's reason for such a type starts with
// "verified network-free:" and the type must be listed here, so a move cannot
// skip the pin; a pinned file that gains a network import fails the test.
var networkFreePins = map[string]struct {
	dirs   []string // relative to internal/; every non-test file of each
	except []string // file names left out (names the exception in the reason)
	files  []string // or just these files of dir
}{
	"agent.memory":      {dirs: []string{"aimemory"}, files: []string{"nodes/agent/memory.go"}},
	"ai.memory":         {dirs: []string{"nodes/aisub"}},
	"data.debug_helper": {files: []string{"nodes/data/debug_helper.go"}},
	"image.edit":        {dirs: []string{"nodes/image"}, except: []string{"remove_background_cgo.go"}},
}

var forbiddenImports = []string{
	"net", "net/http", "net/smtp", "net/rpc", "net/textproto", "os/exec", "crypto/tls",
	"golang.org/x/net", "golang.org/x/crypto/ssh", "github.com/gorilla/websocket",
	"github.com/monoes/mono-agent/internal/monomind", "github.com/monoes/mono-agent/internal/ai/generate",
	"github.com/monoes/mono-agent/internal/jev", "github.com/monoes/mono-agent/internal/jev/jevconf",
	"github.com/monoes/mono-agent/internal/apipkg", "github.com/monoes/mono-agent/internal/nodes/service",
}

func TestNetworkFreeTypesImportNoNetworkPackage(t *testing.T) {
	// every "verified network-free" none type is pinned, and every pin is such a type
	for nodeType, e := range egressTable {
		verified := e.class == workflow.EgressNone && strings.HasPrefix(e.why, "verified network-free:")
		if _, pinned := networkFreePins[nodeType]; verified != pinned {
			t.Errorf("%s: reason says verified=%v but pinned=%v: a type moved to none by reading its code needs an entry in networkFreePins", nodeType, verified, pinned)
		}
	}
	for nodeType, pin := range networkFreePins {
		var files []string
		for _, f := range pin.files {
			files = append(files, filepath.Join("..", f))
		}
		for _, d := range pin.dirs {
			all, _ := filepath.Glob(filepath.Join("..", d, "*.go"))
			for _, f := range all {
				skip := strings.HasSuffix(f, "_test.go")
				for _, x := range pin.except {
					skip = skip || filepath.Base(f) == x
				}
				if !skip {
					files = append(files, f)
				}
			}
		}
		if len(files) == 0 {
			t.Errorf("%s: no file to pin", nodeType)
		}
		for _, f := range files {
			if _, err := os.Stat(f); err != nil {
				t.Errorf("%s: %v", nodeType, err)
				continue
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range parsed.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				for _, bad := range forbiddenImports {
					if path == bad || strings.HasPrefix(path, bad+"/") {
						t.Errorf("%s (%s) imports %q: it is not network-free any more; reclassify it unchecked", nodeType, f, path)
					}
				}
			}
		}
	}
}

// The moved types run in a run that carries a host-bound grant's secret, and
// the sub-node type is allowed where its root resolves it.
func TestNetworkFreeTypesRunInACarryingRun(t *testing.T) {
	noSendGuard(t)
	Build(nil)
	e := newSendEnv(t, secrets.AccessGrant{Hosts: []string{"good.example"}})
	ctx := secrets.AllowResolving(e.ctx, e.db, "default", secrets.AccessOrigin{WorkflowID: "w", ExecutionID: "x"})
	if _, err := secrets.Resolve(secrets.WithAccessNode(ctx, "n", "N"), e.db, "default", "@secret:vs"); err != nil {
		t.Fatal(err)
	}
	ctx = secrets.WithAccessNode(ctx, "m", "M")
	for nodeType := range networkFreePins {
		if class, _ := classifyEgress(nodeType); class != workflow.EgressNone {
			t.Errorf("%s is %v, want none", nodeType, class)
		}
		if err := workflow.CheckEgress(ctx, nodeType, nil, map[string]interface{}{"data": "x"}); err != nil {
			t.Errorf("%s in a carrying run: %v", nodeType, err)
		}
	}
	for _, nodeType := range []string{"core.human_in_loop", "core.evaluation", "image.remove_background", "ai.agent"} {
		if err := workflow.CheckEgress(ctx, nodeType, nil, map[string]interface{}{"data": "x"}); err == nil {
			t.Errorf("%s stays unchecked and must be refused in a carrying run", nodeType)
		}
	}
}
