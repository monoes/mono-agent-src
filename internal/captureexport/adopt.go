package captureexport

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/monoes/mono-agent/internal/capture"
)

// adoptMeta rewrites the provenance an archive claims for a capture it
// brings (R11-12). The archive author chose its Profile and Source, and both
// steer later passes: the profile names an inbox and a knowledge store (and
// is a path component), the source decides what is indexed or excluded. So
// the profile becomes the importing one (opts.Profile, none by default), and
// a source this build does not know becomes "extension". A meta.json that
// needs no change is left byte for byte as it came.
func (im *importer) adoptMeta(dir string, meta *capture.Meta) error {
	changed := false
	if p := strings.TrimSpace(im.opts.Profile); meta.Profile != p {
		meta.Profile, changed = p, true
	}
	switch meta.Source {
	case "", capture.SourceExtension, capture.SourceCrawl, capture.SourceMonobrowse, capture.SourceRecording:
	default:
		meta.Source, changed = capture.SourceExtension, true
	}
	if !changed {
		return nil
	}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("rewrite %s: %w", capture.MetaFile, err)
	}
	if err := os.WriteFile(filepath.Join(dir, capture.MetaFile), append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("rewrite %s: %w", capture.MetaFile, err)
	}
	return nil
}
