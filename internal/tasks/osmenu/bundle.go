package osmenu

import (
	"bytes"
	_ "embed"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"text/template"
)

// Version is the bundle's format, written into every bundle and raised when
// a template changes, so that older bundles read as stale. It is not the
// CLI's release: upgrading monoagentcli does not make a menu stale.
const Version = "1"

// The marker keys of a bundle's Info.plist. A bundle with a profile id there
// is one this package wrote and manages; its identity is the database and the
// profile id (older databases have a profile "default"; new ones start with none).
const (
	KeyCLI       = "MonoAgentTasksCLI"
	KeyDB        = "MonoAgentTasksDB"
	KeyProfileID = "MonoAgentTasksProfileID"
	KeyVersion   = "MonoAgentTasksVersion"
)

// Where a bundle's two files sit inside it (slash-separated).
const (
	InfoPath     = "Contents/Info.plist"
	DocumentPath = "Contents/document.wflow"
)

//go:embed templates/Info.plist.tmpl
var infoTmpl string

//go:embed templates/document.wflow.tmpl
var documentTmpl string

var (
	infoTemplate     = template.Must(template.New("Info.plist").Funcs(funcs).Parse(infoTmpl))
	documentTemplate = template.Must(template.New("document.wflow").Funcs(funcs).Parse(documentTmpl))
)

// Spec is what one menu item is bound to.
type Spec struct {
	CLI         string // monoagentcli's absolute path
	DBPath      string // the database the profile is in, absolute
	ProfileID   string
	ProfileName string
}

// Bundle is a rendered Quick Action: the folder Name holding InfoPath and
// DocumentPath.
type Bundle struct {
	Name      string // "Add to MonoAgent Tasks (<name>).workflow"
	Menu      string // the Services menu item
	DBPath    string
	ProfileID string
	Info      []byte
	Document  []byte
}

// bundleView is what the two property lists are rendered from; the templates
// escape every field.
type bundleView struct {
	BundleID, Menu, CLI, DBPath, ProfileID, Version, Script string
}

// Render renders the menu item for spec. It refuses a path that is not
// absolute, and a path or profile id the bundle cannot carry exactly.
func Render(spec Spec) (Bundle, error) { return render(spec, osascript) }

func render(spec Spec, osa string) (Bundle, error) {
	for _, p := range []struct{ what, path string }{
		{"the monoagentcli path", spec.CLI},
		{"the database path", spec.DBPath},
	} {
		if err := plainValue(p.what, p.path); err != nil {
			return Bundle{}, err
		}
		if !strings.HasPrefix(p.path, "/") {
			return Bundle{}, fmt.Errorf("%s %q is not absolute", p.what, p.path)
		}
	}
	if err := plainValue("the profile id", spec.ProfileID); err != nil {
		return Bundle{}, err
	}
	script, err := renderScript(scriptView{
		CLI: spec.CLI, DBPath: spec.DBPath, ProfileID: spec.ProfileID,
		Name: menuName(spec.ProfileName, spec.ProfileID), Osascript: osa,
	})
	if err != nil {
		return Bundle{}, err
	}
	v := bundleView{
		BundleID: bundleID(spec.ProfileID), Menu: MenuTitle(spec.ProfileName, spec.ProfileID),
		CLI: spec.CLI, DBPath: spec.DBPath, ProfileID: spec.ProfileID, Version: Version, Script: script,
	}
	var info, doc bytes.Buffer
	if err := infoTemplate.Execute(&info, v); err != nil {
		return Bundle{}, err
	}
	if err := documentTemplate.Execute(&doc, v); err != nil {
		return Bundle{}, err
	}
	for _, f := range [][]byte{info.Bytes(), doc.Bytes()} {
		if err := wellFormed(f); err != nil {
			return Bundle{}, fmt.Errorf("the rendered bundle is not well-formed XML: %w", err)
		}
	}
	return Bundle{
		Name: BundleName(spec.ProfileName, spec.ProfileID), Menu: v.Menu, DBPath: spec.DBPath, ProfileID: spec.ProfileID,
		Info: info.Bytes(), Document: doc.Bytes(),
	}, nil
}

// wellFormed reads data through with an XML reader: the last check that no
// value broke a property list (a character XML cannot hold, say).
func wellFormed(data []byte) error {
	d := xml.NewDecoder(bytes.NewReader(data))
	for {
		_, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
