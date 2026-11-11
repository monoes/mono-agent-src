package noderegistry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// storePackages are the internal packages a node writes through without
// calling os or sql itself: the chat memory, application and document
// persistence, the data tables, the profile database.
var storePackages = []string{"aimemory", "apply", "documents", "applications", "datatable", "storage", "people", "commsync"}

// writeCallsByDir finds, per node package directory, the files that write
// to a file, a database or the keyring, or use the chat-memory store.
func writeCallsByDir(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	_ = filepath.WalkDir("../nodes", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if perr != nil {
			t.Errorf("%s: %v", p, perr)
			return nil
		}
		hit := ""
		for _, im := range f.Imports {
			path := strings.Trim(im.Path.Value, `"`)
			for _, store := range storePackages {
				if strings.HasSuffix(path, "/internal/"+store) {
					hit = "imports " + store
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			s, ok := c.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pk := ""
			if id, ok := s.X.(*ast.Ident); ok {
				pk = id.Name
			}
			switch {
			case pk == "os" && strings.Contains(" WriteFile Create OpenFile Rename Mkdir MkdirAll CreateTemp ", " "+s.Sel.Name+" "),
				pk == "ioutil" && s.Sel.Name == "WriteFile",
				pk == "keyring" && s.Sel.Name == "Set",
				s.Sel.Name == "Exec" || s.Sel.Name == "ExecContext":
				hit = pk + "." + s.Sel.Name
			}
			return true
		})
		if hit != "" {
			dir := filepath.ToSlash(filepath.Dir(p))
			out[dir] = append(out[dir], filepath.Base(p)+" ("+hit+")")
		}
		return nil
	})
	return out
}

// noStoreWrite are the local-only types, in a package that writes files, a
// database or the memory store somewhere, that do not themselves put what
// they are given where another run can read it back, and why.
var noStoreWrite = map[string]string{
	"applications.list":       "reads application records; applications.create, set_status and tag write them and declare it",
	"core.aggregate":          "passes or reshapes items in memory; the package writes the human-in-the-loop card, the data table and static data elsewhere (declared or not a channel)",
	"core.code":               "passes or reshapes items in memory; the package writes the human-in-the-loop card, the data table and static data elsewhere (declared or not a channel)",
	"core.compare_datasets":   "passes or reshapes items in memory; the package writes the human-in-the-loop card, the data table and static data elsewhere (declared or not a channel)",
	"core.execute_workflow":   "passes or reshapes items in memory; the package writes the human-in-the-loop card, the data table and static data elsewhere (declared or not a channel)",
	"core.filter":             "passes or reshapes items in memory; the package writes the human-in-the-loop card, the data table and static data elsewhere (declared or not a channel)",
	"core.if":                 "passes or reshapes items in memory; the package writes the human-in-the-loop card, the data table and static data elsewhere (declared or not a channel)",
	"core.limit":              "passes or reshapes items in memory; the package writes the human-in-the-loop card, the data table and static data elsewhere (declared or not a channel)",
	"core.merge":              "passes or reshapes items in memory; the package writes the human-in-the-loop card, the data table and static data elsewhere (declared or not a channel)",
	"core.noop":               "passes or reshapes items in memory; the package writes the human-in-the-loop card, the data table and static data elsewhere (declared or not a channel)",
	"core.remove_duplicates":  "writes only sha256 hashes and a watermark to static data: not a channel for a value",
	"core.set":                "passes or reshapes items in memory; the package writes the human-in-the-loop card, the data table and static data elsewhere (declared or not a channel)",
	"core.sort":               "passes or reshapes items in memory; the package writes the human-in-the-loop card, the data table and static data elsewhere (declared or not a channel)",
	"core.split_in_batches":   "passes or reshapes items in memory; the package writes the human-in-the-loop card, the data table and static data elsewhere (declared or not a channel)",
	"core.stop_error":         "passes or reshapes items in memory; the package writes the human-in-the-loop card, the data table and static data elsewhere (declared or not a channel)",
	"core.switch":             "passes or reshapes items in memory; the package writes the human-in-the-loop card, the data table and static data elsewhere (declared or not a channel)",
	"core.wait":               "passes or reshapes items in memory; the package writes the human-in-the-loop card, the data table and static data elsewhere (declared or not a channel)",
	"data.crypto":             "transforms items in memory; the package's file writers declare WritesLocalStore() themselves",
	"data.datetime":           "transforms items in memory; the package's file writers declare WritesLocalStore() themselves",
	"data.debug_helper":       "transforms items in memory; the package's file writers declare WritesLocalStore() themselves",
	"data.extract_from_file":  "transforms items in memory; the package's file writers declare WritesLocalStore() themselves",
	"data.html":               "transforms items in memory; the package's file writers declare WritesLocalStore() themselves",
	"data.ical":               "transforms items in memory; the package's file writers declare WritesLocalStore() themselves",
	"data.jwt":                "transforms items in memory; the package's file writers declare WritesLocalStore() themselves",
	"data.markdown":           "transforms items in memory; the package's file writers declare WritesLocalStore() themselves",
	"data.rename_keys":        "transforms items in memory; the package's file writers declare WritesLocalStore() themselves",
	"data.split_out":          "transforms items in memory; the package's file writers declare WritesLocalStore() themselves",
	"data.summarize":          "transforms items in memory; the package's file writers declare WritesLocalStore() themselves",
	"data.totp":               "transforms items in memory; the package's file writers declare WritesLocalStore() themselves",
	"data.xml":                "transforms items in memory; the package's file writers declare WritesLocalStore() themselves",
	"image.adjust":            "edits pixels, not text; image.vault_save declares WritesLocalStore()",
	"image.convert":           "edits pixels, not text; image.vault_save declares WritesLocalStore()",
	"image.crop":              "edits pixels, not text; image.vault_save declares WritesLocalStore()",
	"image.info":              "edits pixels, not text; image.vault_save declares WritesLocalStore()",
	"image.remove_background": "edits pixels, not text; image.vault_save declares WritesLocalStore()",
	"image.resize":            "edits pixels, not text; image.vault_save declares WritesLocalStore()",
	"image.thumbnail":         "edits pixels, not text; image.vault_save declares WritesLocalStore()",
	"image.vault_get":         "edits pixels, not text; image.vault_save declares WritesLocalStore()",
	"vector.document_loader":  "splits or loads text in memory; vector.store (publishing) is what writes a store",
	"vector.embeddings":       "splits or loads text in memory; vector.store (publishing) is what writes a store",
	"vector.load_document":    "splits or loads text in memory; vector.store (publishing) is what writes a store",
	"vector.split_text":       "splits or loads text in memory; vector.store (publishing) is what writes a store",
	"vector.text_splitter":    "splits or loads text in memory; vector.store (publishing) is what writes a store",
}

// Every local-only type in a node package that writes a file, a database or
// the chat-memory store either declares WritesLocalStore() (a tainted run's
// write of a value it read is refused) or is listed with why it does not
// write one. Found by reading the packages' source, so a new writer in an
// existing package, or a new type in one, cannot be forgotten.
func TestEveryLocalOnlyTypeInAWritingPackageIsAStoreWriterOrListed(t *testing.T) {
	writes := writeCallsByDir(t)
	registry := Build(nil)
	var missing []string
	used := map[string]bool{}
	for _, nt := range registry.Types() {
		if pub, ok := classifiedPublishing(nt); !ok || pub {
			continue
		}
		factory, _ := registry.Get(nt)
		exec := factory()
		rt := reflect.TypeOf(exec)
		for rt.Kind() == reflect.Ptr {
			rt = rt.Elem()
		}
		dir := "../" + strings.TrimPrefix(rt.PkgPath(), "github.com/monoes/mono-agent/internal/")
		if len(writes[dir]) == 0 {
			continue
		}
		if d, ok := exec.(declaresStore); ok && d.WritesLocalStore() {
			continue
		}
		if _, ok := noStoreWrite[nt]; ok {
			used[nt] = true
			continue
		}
		missing = append(missing, nt+" ("+dir+": "+strings.Join(writes[dir], ", ")+")")
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("%s: declare WritesLocalStore() or list it in noStoreWrite with why it writes no store", m)
	}
	for nt := range noStoreWrite {
		if !used[nt] {
			t.Errorf("%s is listed in noStoreWrite but is not a local-only type in a writing package (or declares the store write): remove it", nt)
		}
	}
}
