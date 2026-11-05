package node

import (
	"reflect"
	"testing"

	"github.com/monoes/mono-agent/internal/apipkg"
	"github.com/monoes/mono-agent/internal/noderegistry"
)

// The tests share one HOME so the API packages are parsed once (nodeVaultTestDB). This holds
// that sharing to what a HOME of its own gives: the node set, the aliases and the parsed
// packages are the same as a fresh parse of the built-in catalog.
func TestSharedHomeRegistryEqualsAFreshOne(t *testing.T) {
	nodeVaultTestDB(t)
	shared := noderegistry.Build(nil)
	apipkg.Reload()
	t.Cleanup(apipkg.Reload)
	fresh := noderegistry.Build(nil)
	if !reflect.DeepEqual(shared.Types(), fresh.Types()) {
		t.Errorf("node sets differ: %d shared, %d fresh", len(shared.Types()), len(fresh.Types()))
	}
	if !reflect.DeepEqual(shared.Aliases(), fresh.Aliases()) {
		t.Error("aliases differ")
	}
	cached, err := apipkg.Load()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := apipkg.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cached, catalog) {
		t.Error("the loaded packages (schemas included) differ from a fresh parse of the catalog")
	}
}
