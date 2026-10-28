package main

import (
	"context"
	"embed"
	"github.com/monoes/mono-agent/internal/vault"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/gitsafe"
	"github.com/monoes/mono-agent/internal/nodemgr"
	"github.com/monoes/mono-agent/internal/shellpath"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

// Version is injected at build time via ldflags.
// Falls back to git describe at runtime when built without ldflags (e.g. wails dev).
var (
	version   = ""
	buildDate = ""
)

func init() {
	if version == "" {
		if out, err := gitsafe.Command(context.Background(), "describe", "--tags", "--always").Output(); err == nil {
			version = strings.TrimSpace(string(out))
		} else {
			version = "dev"
		}
	}
	if buildDate == "" {
		buildDate = time.Now().UTC().Format("2006-01-02T15:04:05Z")
	}
}

// enableDevTools controls whether the WebKit inspector opens on startup.
// Set to true temporarily while debugging frontend issues.
const enableDevTools = false

//go:embed all:frontend/dist
var assets embed.FS

// appIcon is the window/taskbar icon for Linux. macOS takes its icon from the
// .app bundle and Windows from the embedded resource, so neither needs this.
//
//go:embed build/appicon.png
var appIcon []byte

// vaultImageHandler serves the active profile's vault images at
// /vault-image/<id> (the url `image list` gives each image; the older
// /vault-image/<filename> form still resolves), from each image's stored
// path, so one profile cannot enumerate or view another profile's vault
// images.
func vaultImageHandler(app *App) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/vault-image/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if app.signedOut() != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		// Use filepath.Base to prevent path traversal.
		name := filepath.Base(r.URL.Path)

		if app.db == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		profileID, err := app.profileID()
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Only this profile's files, served from the path stored on the row
		// (images live in the profile's own vault folder). The lookup is the
		// vault package's, which the CLI's `image` commands use too; it stays
		// in-process because it runs once per <img> request.
		path, ok, err := vault.ImagePathInProfile(r.Context(), app.db, profileID, name)
		if err != nil || !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		http.ServeFile(w, r, path)
	})
}

func main() {
	// Launched from Finder/Dock the app gets launchd's bare PATH, so
	// monomind, node, claude and npm (nvm/Homebrew installs) are all
	// invisible to it and to every monoagentcli child. Import the login
	// shell's PATH before anything shells out.
	shellpath.Apply(context.Background())
	// A Node that monoagent installed itself (Settings › System health)
	// goes on PATH too — after the login PATH, so a suitable system Node
	// still wins.
	nodemgr.Activate(context.Background())

	app := NewApp()

	err := wails.Run(&options.App{
		Title:            "Mono Agent",
		Width:            1440,
		Height:           900,
		MinWidth:         1100,
		MinHeight:        700,
		BackgroundColour: &options.RGBA{R: 4, G: 6, B: 10, A: 255},
		AssetServer: &assetserver.Options{
			Assets:  assets,
			Handler: vaultImageHandler(app),
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind:       []interface{}{app},
		Debug: options.Debug{
			OpenInspectorOnStartup: enableDevTools,
		},
		Linux: &linux.Options{
			// Wails only calls gtk_window_set_icon when options.Linux is
			// non-nil (frontend/desktop/linux/window.go), so without this
			// block the window and its dock/taskbar entry have no icon.
			Icon: appIcon,

			// A desktop entry is matched to its window by StartupWMClass,
			// which GTK derives from g_get_prgname() — by default the
			// executable's basename. Ours differs per build path
			// (monoagent-ui from wails.json, MonoAgent from the Makefile,
			// MonoAgent-linux-amd64 from the release tarball), so pin it
			// here and one .desktop file matches however it was installed.
			ProgramName: "monoagent",

			// Wails forces this to Never whenever options.Linux is nil, as a
			// workaround for wailsapp/wails#2977 (blank/garbled webview under
			// some GPU drivers). Populating the struct at all opts out of that
			// default, so restate it rather than silently switching to
			// OnDemand — the zero value — as a side effect of setting an icon.
			WebviewGpuPolicy: linux.WebviewGpuPolicyNever,
		},
		Mac: &mac.Options{
			TitleBar: &mac.TitleBar{
				TitlebarAppearsTransparent: true,
				HideTitle:                  true,
				FullSizeContent:            true,
			},
			WebviewIsTransparent: true,
			WindowIsTranslucent:  true,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			DisableWindowIcon:    false,
		},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}
