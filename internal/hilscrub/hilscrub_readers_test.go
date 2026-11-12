package hilscrub

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var cardTextRE = regexp.MustCompile("(?s)SELECT[^`]*\\b(readonly_data|editable_data|edited_data)\\b[^`]*(FROM|JOIN) hil_pending")

// Every reader of a card's text columns either calls hilscrub.Ensure (and, for
// what it shows, hilscrub.Mask) or says why the card it reads is never
// scrubbed. A new reader fails until it is decided; a listed one fails when
// its call is deleted.
func TestEveryReaderOfACardsTextIsAccountedFor(t *testing.T) {
	ensures := map[string]string{ // file -> the call it must hold
		"internal/hilsend/store.go":              "hilscrub.Ensure(",
		"internal/cli/hil/hil.go":                "hilscrub.Ensure(",
		"internal/httpapi/handlers_nodes_hil.go": "hilscrub.Ensure(",
		"internal/mcp/tools_nodes_hil.go":        "hilscrub.Ensure(",
		"internal/orgdecide/hil.go":              "hilscrub.Ensure(",
	}
	exempt := map[string]string{
		"internal/hilscrub/hilscrub.go":           "the scrub itself",
		"internal/browserctl/gate.go":             "a browser card (browser:): never scrubbed, holds no send text",
		"internal/browserctl/hil.go":              "browser cards about a page's form",
		"internal/cli/hil/browser_submit.go":      "browser cards",
		"internal/nodes/control/human_in_loop.go": "reads the answer of its own item, edited by the person",
	}
	root, _ := filepath.Abs("../..")
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, _ := os.ReadFile(path)
		if !cardTextRE.Match(b) {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if _, ok := exempt[rel]; ok {
			return nil
		}
		call, ok := ensures[rel]
		if !ok {
			t.Errorf("%s reads a card's text and is not accounted for: call hilscrub.Ensure before it reads, then list it", rel)
			return nil
		}
		if !strings.Contains(string(b), call) {
			t.Errorf("%s no longer calls %s before it reads a card's text", rel, call)
		}
		return nil
	})
	// The listed files still read cards (a stale entry hides a moved reader).
	for rel := range ensures {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || !cardTextRE.Match(b) {
			t.Errorf("%s is listed as a reader of card text and no longer is: update the list", rel)
		}
	}
}
