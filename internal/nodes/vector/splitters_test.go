package vector

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

func TestSplitCharacterExactBoundaries(t *testing.T) {
	cases := []struct {
		name          string
		text, sep     string
		size, overlap int
		want          []string
	}{
		{"overlap one word", "aaa bbb ccc ddd", " ", 7, 3, []string{"aaa bbb", "bbb ccc", "ccc ddd"}},
		{"no overlap", "aaa bbb ccc ddd", " ", 7, 0, []string{"aaa bbb", "ccc ddd"}},
		{"paragraphs", "one\n\ntwo\n\nthree", "\n\n", 8, 0, []string{"one\n\ntwo", "three"}},
		{"oversized piece kept whole", "abcdefghij xy", " ", 5, 0, []string{"abcdefghij", "xy"}},
		{"runes not bytes", "ééé ààà", " ", 7, 0, []string{"ééé ààà"}},
		{"empty pieces dropped", "a  b", " ", 3, 0, []string{"a b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitCharacter(c.text, c.sep, c.size, c.overlap)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestSplitRecursiveExactBoundaries(t *testing.T) {
	text := "Para one is here.\n\nSecond para that is longer than limit."
	got := splitRecursive(text, defaultRecursiveSeparators, 20, 0)
	want := []string{"Para one is here.", "Second para that is", "longer than limit."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}

	// Overlap carries the trailing word(s) forward.
	got = splitRecursive("one two three four five", defaultRecursiveSeparators, 9, 4)
	want = []string{"one two", "two three", "four five"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("overlap: got %q, want %q", got, want)
	}

	// A word longer than the chunk falls through to character splitting.
	got = splitRecursive("abcdefgh", defaultRecursiveSeparators, 3, 0)
	want = []string{"abc", "def", "gh"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("char fallback: got %q, want %q", got, want)
	}
}

func TestApproxTokensRoundTripAndBoundaries(t *testing.T) {
	text := "Hello world, 12345 tokenization!"
	toks := approxTokens(text)
	want := []string{"Hell", "o", " worl", "d", ",", " 123", "45", " toke", "niza", "tion", "!"}
	if !reflect.DeepEqual(toks, want) {
		t.Fatalf("tokens %q, want %q", toks, want)
	}
	if strings.Join(toks, "") != text {
		t.Fatal("tokens do not reproduce the text")
	}
	got := splitTokens(text, 4, 1)
	wantChunks := []string{"Hello world", "d, 12345", "45 tokenization", "tion!"}
	if !reflect.DeepEqual(got, wantChunks) {
		t.Fatalf("chunks %q, want %q", got, wantChunks)
	}
}

func TestSplitTextNode(t *testing.T) {
	n := &SplitTextNode{}
	in := workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{
		"body":     "aaa bbb ccc ddd",
		"url":      "https://x.test/a",
		"metadata": map[string]interface{}{"lang": "en"},
	})}}
	out, err := n.Execute(context.Background(), in, map[string]interface{}{
		"splitter": "character", "separator": " ", "chunk_size": float64(7), "chunk_overlap": float64(3),
		"input_field": "body", "metadata_fields": "url",
	})
	if err != nil {
		t.Fatal(err)
	}
	items := out[0].Items
	if len(items) != 3 {
		t.Fatalf("want 3 chunks, got %d", len(items))
	}
	if items[1].JSON["pageContent"] != "bbb ccc" {
		t.Fatalf("chunk 1 = %v", items[1].JSON["pageContent"])
	}
	meta := items[2].JSON["metadata"].(map[string]interface{})
	if meta["lang"] != "en" || meta["url"] != "https://x.test/a" || meta["chunk_index"] != 2 || meta["chunk_count"] != 3 {
		t.Fatalf("metadata = %v", meta)
	}
}

func TestSplitConfigErrors(t *testing.T) {
	for _, c := range []struct {
		cfg  map[string]interface{}
		want string
	}{
		{map[string]interface{}{"chunk_size": float64(10), "chunk_overlap": float64(10)}, "chunk_overlap (10) must be smaller than chunk_size (10)"},
		{map[string]interface{}{"splitter": "semantic"}, `splitter "semantic" is not supported`},
		{map[string]interface{}{"chunk_size": "ten"}, "chunk_size must be a whole number"},
		{map[string]interface{}{"separators": []interface{}{float64(1)}}, "separators must be a list of strings"},
	} {
		if _, err := parseSplitConfig(c.cfg); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("cfg %v: err %v, want %q", c.cfg, err, c.want)
		}
	}
	sc, err := parseSplitConfig(map[string]interface{}{"separators": `\n\n` + "\n" + `\n`})
	if err != nil || !reflect.DeepEqual(sc.separators, []string{"\n\n", "\n"}) {
		t.Fatalf("separators from text: %q %v", sc.separators, err)
	}
}
