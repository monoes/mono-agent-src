package capturesummary

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/capture"
)

func TestPromptWebPageStructureAndEnglish(t *testing.T) {
	in := &Input{
		Kind:    KindPage,
		Title:   "El Faro de Dunmore",
		URL:     "https://paper.test/faro",
		Source:  "readable.md",
		Content: "El farero llevó un libro de registro durante cuarenta años.",
	}
	p := Prompt(in)

	// Must instruct English regardless of source language
	if !strings.Contains(p, "Write Markdown always in English, regardless of the language of the source text, captions, or video") {
		t.Errorf("prompt missing English instruction:\n%s", p)
	}

	// Must have Super summary on top, then Summary, then Key points
	superIdx := strings.Index(p, "## Super summary")
	summaryIdx := strings.Index(p, "## Summary")
	keyPointsIdx := strings.Index(p, "## Key points")

	if superIdx == -1 || summaryIdx == -1 || keyPointsIdx == -1 {
		t.Fatalf("prompt missing required sections: super=%d summary=%d keyPoints=%d", superIdx, summaryIdx, keyPointsIdx)
	}

	if !(superIdx < summaryIdx && summaryIdx < keyPointsIdx) {
		t.Errorf("sections not in expected order (super < summary < keyPoints): super=%d summary=%d keyPoints=%d", superIdx, summaryIdx, keyPointsIdx)
	}

	if strings.Contains(p, "## Highlights") {
		t.Error("web page prompt should not include Highlights")
	}
}

func TestPromptVideoStructureAndEnglish(t *testing.T) {
	in := &Input{
		Kind:    KindVideo,
		Title:   "Vidéo en Français",
		URL:     "https://www.youtube.com/watch?v=xyz",
		Source:  "transcript.md",
		Content: "[0:01](https://www.youtube.com/watch?v=xyz&t=1s) Bonjour tout le monde",
		Video: &videoMeta{
			Channel:  "Creator",
			Duration: "10:00",
		},
	}
	p := Prompt(in)

	if !strings.Contains(p, "YouTube video") {
		t.Errorf("prompt missing YouTube video identifier:\n%s", p)
	}

	if !strings.Contains(p, "Write Markdown always in English, regardless of the language of the source text, captions, or video") {
		t.Errorf("prompt missing English instruction:\n%s", p)
	}

	superIdx := strings.Index(p, "## Super summary")
	summaryIdx := strings.Index(p, "## Summary")
	keyPointsIdx := strings.Index(p, "## Key points")
	highlightsIdx := strings.Index(p, "## Highlights")

	if superIdx == -1 || summaryIdx == -1 || keyPointsIdx == -1 || highlightsIdx == -1 {
		t.Fatalf("prompt missing required sections: super=%d summary=%d keyPoints=%d highlights=%d", superIdx, summaryIdx, keyPointsIdx, highlightsIdx)
	}

	if !(superIdx < summaryIdx && summaryIdx < keyPointsIdx && keyPointsIdx < highlightsIdx) {
		t.Errorf("sections not in expected order: super=%d summary=%d keyPoints=%d highlights=%d", superIdx, summaryIdx, keyPointsIdx, highlightsIdx)
	}
}

func TestLoadInputDoesNotLoadAHugeReadableWhole(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "readable.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(64 << 20); err != nil {
		t.Fatal(err)
	}
	f.Close()
	// A sparse file reads as NULs, which are valid UTF-8; give it text.
	if err := os.WriteFile(filepath.Join(dir, "readable.md"), []byte(strings.Repeat("word ", 64<<17)), 0o644); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	in, err := LoadInput(dir, capture.Meta{URL: "https://a.test/"}, KindPage, 1000)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if !in.Truncated {
		t.Error("a clipped input must say so")
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > 16<<20 {
		t.Errorf("LoadInput allocated %d MiB for a 1000-char input", got>>20)
	}
}
