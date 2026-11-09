package apipkg

import (
	"github.com/monoes/mono-agent/internal/testhome"
	"strings"
	"testing"
)

// TestCatalog is the harness every catalog package must pass:
//
//	go test ./internal/apipkg -run 'TestCatalog/<id>'
//
// For each catalog/<id>.json: the file validates, every operation has a case
// in catalog/testdata/<id>.json, each case's request (method, path, query,
// body, auth) matches its expect block, and each required field is
// enforced.
func TestCatalog(t *testing.T) {
	files, err := CatalogFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("catalog/ has no packages")
	}
	for _, name := range files {
		id := strings.TrimSuffix(name, ".json")
		t.Run(id, func(t *testing.T) {
			b, err := catalogFS.ReadFile("catalog/" + name)
			if err != nil {
				t.Fatal(err)
			}
			p, err := Parse(b, "catalog/"+name)
			if err != nil {
				t.Fatalf("%v\n  (check with: go run ./cmd/monoagentcli integration validate internal/apipkg/catalog/%s)", err, name)
			}
			if p.ID != id {
				t.Fatalf("catalog/%s: id is %q; it must equal the file name %q", name, p.ID, id)
			}
			cases, err := loadCases(id)
			if err != nil {
				t.Fatalf("catalog/%s needs test cases: %v\n  (write catalog/testdata/%s.json: {\"<resource>.<operation>\": {\"params\": {…}, \"expect\": {\"method\": …, \"path\": …}}})", name, err, id)
			}
			for _, k := range sortedCaseKeys(cases) {
				if _, ok := opByKey(p, k); !ok {
					t.Errorf("catalog/testdata/%s.json: case %q names no operation of the package", id, k)
				}
			}
			for i := range p.Operations {
				op := &p.Operations[i]
				t.Run(op.Key(), func(t *testing.T) {
					c, ok := cases[op.Key()]
					if !ok {
						t.Fatalf("catalog/testdata/%s.json has no case %q: add one with sample params and the expected request", id, op.Key())
					}
					checkCase(t, p, op, c)
					checkRequiredFields(t, p, op, c)
				})
			}
		})
	}
}

func opByKey(p *Package, key string) (*Operation, bool) {
	for i := range p.Operations {
		if p.Operations[i].Key() == key {
			return &p.Operations[i], true
		}
	}
	return nil, false
}

// TestCatalogLoads: Load sees every catalog package with no errors, and
// the node types and n8n types are unique.
func TestCatalogLoads(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	Reload()
	t.Cleanup(Reload)
	pkgs, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	files, _ := CatalogFiles()
	if len(pkgs) != len(files) {
		t.Fatalf("Load returned %d packages, catalog has %d files", len(pkgs), len(files))
	}
	n8n := map[string]string{}
	for _, p := range pkgs {
		for _, nt := range append(append([]string{}, p.N8nTypes...), p.N8nTriggerTypes...) {
			if other, ok := n8n[nt]; ok {
				t.Errorf("n8n type %s is claimed by %s and %s", nt, other, p.ID)
			}
			n8n[nt] = p.ID
		}
	}
}
