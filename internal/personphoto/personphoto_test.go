package personphoto

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
	"github.com/monoes/mono-agent/internal/vault"
)

func TestMain(m *testing.M) { testhome.Main(m) }

// a 1x1 PNG
const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

func newDB(t *testing.T) *storage.Database {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	return db
}

func TestLocalizeSavesPhotoAndKeepsSource(t *testing.T) {
	allowPrivate = true
	t.Cleanup(func() { allowPrivate = false })
	png, _ := base64.StdEncoding.DecodeString(pngB64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gone.jpg" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/page.jpg" {
			w.Write([]byte("<html>not an image</html>"))
			return
		}
		w.Write(png)
	}))
	defer srv.Close()
	t.Cleanup(vault.Wait)

	db := newDB(t)
	for u, url := range map[string]string{"ada": srv.URL + "/ada.jpg", "gone": srv.URL + "/gone.jpg", "html": srv.URL + "/page.jpg"} {
		if _, err := db.DB.Exec(`INSERT INTO people (id, platform_username, platform, image_url, profile_id) VALUES (?, ?, 'X', ?, 'default')`, "id-"+u, u, url); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	saved, errs := Localize(ctx, db.DB, "default", nil)
	if saved != 1 || len(errs) != 2 {
		t.Fatalf("saved %d, errs %v; want 1 saved, 2 errors", saved, errs)
	}

	var img, src, imgID string
	db.DB.QueryRow(`SELECT image_url, json_extract(profile_details,'$.photo_source_url'), json_extract(profile_details,'$.photo_image_id') FROM people WHERE id='id-ada'`).Scan(&img, &src, &imgID)
	if !strings.HasPrefix(img, "/vault-image/") || src != srv.URL+"/ada.jpg" || img != "/vault-image/"+imgID {
		t.Fatalf("ada: image_url %q source %q id %q", img, src, imgID)
	}
	path, ok, err := vault.ImagePathInProfile(ctx, db.DB, "default", imgID)
	if err != nil || !ok {
		t.Fatalf("vault image %s: %v %v", imgID, ok, err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != string(png) {
		t.Fatalf("saved file differs: %v", err)
	}
	Delete(ctx, db.DB, "default", []string{"id-ada", "id-gone"})
	var left int
	db.DB.QueryRow(`SELECT COUNT(*) FROM vault_images WHERE source = ?`, Source).Scan(&left)
	if left != 0 {
		t.Fatalf("%d saved photos left after Delete", left)
	}
	db.DB.Exec(`UPDATE people SET image_url = ?, profile_details = NULL WHERE id='id-ada'`, srv.URL+"/ada.jpg")
	Localize(ctx, db.DB, "default", []Ref{{"X", "ada"}})
	var gone string
	db.DB.QueryRow(`SELECT image_url FROM people WHERE id='id-gone'`).Scan(&gone)
	if gone != srv.URL+"/gone.jpg" {
		t.Fatalf("unfetchable photo lost its URL: %q", gone)
	}

	// A re-scrape sets the remote URL again: the new copy replaces the old one.
	db.DB.Exec(`UPDATE people SET image_url = ? WHERE id='id-ada'`, srv.URL+"/ada-new.jpg")
	if saved, errs := Localize(ctx, db.DB, "default", []Ref{{"X", "ada"}}); saved != 1 || len(errs) != 0 {
		t.Fatalf("second run: %d %v", saved, errs)
	}
	var n int
	db.DB.QueryRow(`SELECT COUNT(*) FROM vault_images WHERE source = ?`, Source).Scan(&n)
	if n != 1 {
		t.Fatalf("%d profile-photo images after a re-save; want the old one removed", n)
	}
}

func TestLocalizeKeepsSavedPhotoWhenRefetchFailsOrURLIsUnchanged(t *testing.T) {
	allowPrivate = true
	t.Cleanup(func() { allowPrivate = false })
	png, _ := base64.StdEncoding.DecodeString(pngB64)
	hits, fail := 0, false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if fail {
			http.Error(w, "expired", http.StatusForbidden)
			return
		}
		w.Write(png)
	}))
	defer srv.Close()
	t.Cleanup(vault.Wait)
	db := newDB(t)
	url := srv.URL + "/ada.jpg"
	db.DB.Exec(`INSERT INTO people (id, platform_username, platform, image_url, profile_id) VALUES ('p', 'ada', 'X', ?, 'default')`, url)
	ctx := context.Background()
	if n, errs := Localize(ctx, db.DB, "default", nil); n != 1 || len(errs) != 0 {
		t.Fatalf("first: %d %v", n, errs)
	}
	var local string
	db.DB.QueryRow(`SELECT image_url FROM people WHERE id='p'`).Scan(&local)

	// A re-scrape writes the same remote URL back: no second download.
	db.DB.Exec(`UPDATE people SET image_url = ? WHERE id='p'`, url)
	before := hits
	Localize(ctx, db.DB, "default", nil)
	var got string
	db.DB.QueryRow(`SELECT image_url FROM people WHERE id='p'`).Scan(&got)
	if got != local || hits != before {
		t.Fatalf("unchanged URL: image_url %q (want %q), %d extra downloads", got, local, hits-before)
	}

	// A new URL that can't be fetched leaves the saved photo in place.
	fail = true
	db.DB.Exec(`UPDATE people SET image_url = ? WHERE id='p'`, srv.URL+"/new.jpg")
	Localize(ctx, db.DB, "default", nil)
	db.DB.QueryRow(`SELECT image_url FROM people WHERE id='p'`).Scan(&got)
	if got != local {
		t.Fatalf("failed fetch: image_url %q, want the saved %q", got, local)
	}
}

func TestLocalizeRefusesNonPublicAddresses(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	db := newDB(t)
	db.DB.Exec(`INSERT INTO people (id, platform_username, platform, image_url, profile_id) VALUES ('p', 'ada', 'X', ?, 'default')`, srv.URL+"/a.jpg")
	n, errs := Localize(context.Background(), db.DB, "default", nil)
	if n != 0 || len(errs) != 1 || hit {
		t.Fatalf("saved %d, errs %v, server hit %v; want the loopback fetch refused", n, errs, hit)
	}
}

func TestPublicIPRefusesReservedRanges(t *testing.T) {
	for ip, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700:4700::1111": true,
		"100.64.0.1": false, "100.100.100.100": false, "198.18.0.1": false, "192.0.0.8": false,
		"169.254.169.254": false, "::ffff:100.64.0.1": false, "10.0.0.1": false, "127.0.0.1": false,
	} {
		if got := publicIP(net.ParseIP(ip)); got != want {
			t.Errorf("publicIP(%s) = %v, want %v", ip, got, want)
		}
	}
}
