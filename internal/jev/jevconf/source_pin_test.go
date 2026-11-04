package jevconf

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Every way the TypeSafe key leaves the process goes through this package:
// ResolveKey reads it (the grant check and audit apply to the implicit lookup
// as to an explicit @secret: reference) and ClientFor pairs it with its host
// (BaseURL). A new caller that builds its own client or reads
// TYPESAFE_BASE_URL itself would bypass both; this pin fails for it.
func TestEveryUserOfTheTypeSafeKeyGoesThroughJevconf(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(here), "..", "..", "..")
	// keyOnlyChecks may call ResolveKey without building a client: they only
	// look whether a key exists.
	keyOnlyChecks := map[string]string{"internal/cli/org/autonomy_decider.go": "reports whether a key resolves, sends nothing"}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if n := d.Name(); n != "." && (strings.HasPrefix(n, ".") || n == "node_modules" || n == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasPrefix(rel, "internal/jev/") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		src := string(b)
		for _, bad := range []string{"jev.NewClient(", "jev.Client{", `Getenv("TYPESAFE_BASE_URL")`, `LookupEnv("TYPESAFE_BASE_URL")`} {
			if strings.Contains(src, bad) {
				t.Errorf("%s builds a TypeSafe client or reads its host itself (%s): use jevconf.NewClient, or jevconf.ResolveKey + jevconf.ClientFor, so the key goes only where jevconf.BaseURL says and its read is gated", rel, bad)
			}
		}
		if strings.Contains(src, "jevconf.ResolveKey(") && !strings.Contains(src, "jevconf.ClientFor(") {
			if _, ok := keyOnlyChecks[rel]; !ok {
				t.Errorf("%s resolves the TypeSafe key but does not build its client with jevconf.ClientFor", rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// The read of a stored key is gated: the lookup refuses an unapproved step
	// and goes through secrets.Resolve under agent access (readEntry).
	b, err := os.ReadFile(filepath.Join(filepath.Dir(here), "key.go"))
	if err != nil {
		t.Fatal(err)
	}
	key := string(b)
	if !strings.Contains(key, "secrets.RefusedReference(ctx, secretRef(c.name))") || !strings.Contains(key, "readEntry(ctx, db, c.scope, c.name)") {
		t.Error("ResolveKey no longer refuses and audits the read of the stored key before using it")
	}
	if n := strings.Count(key, "entryValue("); n != 3 { // its definition, readEntry, Adopt
		t.Errorf("entryValue( appears %d times in key.go, want 3 (definition, readEntry, Adopt): a new caller reads the vault key without the gate", n)
	}

	// vector.store's embedding block sends its provider key only to the host
	// its connection or the user's workflow names.
	vec, err := os.ReadFile(filepath.Join(root, "internal/nodes/vector/credentials.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(vec), "workflow.CheckDestinationKeys(") {
		t.Error("internal/nodes/vector/credentials.go no longer checks where a connection's key goes")
	}
}
