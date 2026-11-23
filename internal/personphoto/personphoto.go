// Package personphoto saves people's profile photos locally. Platforms give
// a photo as a CDN URL that expires (Instagram, LinkedIn) or refuses
// hotlinking, so a stored URL soon shows nothing; Localize downloads the
// image into the profile's vault and points people.image_url at it.
package personphoto

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/monoes/mono-agent/internal/vault"
)

// Source is the vault_images.source of a saved profile photo.
const Source = "profile-photo"

const (
	maxBytes    = 10 << 20
	concurrency = 4
	userAgent   = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36"
)

// budget bounds one Localize call, so a big batch can't hold a scrape up.
const budget = 2 * time.Minute

// allowPrivate lets tests fetch from a loopback server.
var allowPrivate bool

// client refuses to connect to a non-public address (loopback, private,
// link-local), after redirects and DNS answers included: photo URLs come
// from scraped pages.
var client = &http.Client{
	Timeout: 20 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
			Control: func(_, address string, _ syscall.RawConn) error {
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					return err
				}
				ip := net.ParseIP(host)
				if ip == nil || !allowPrivate && !publicIP(ip) {
					return errors.New("refusing to fetch from a non-public address")
				}
				return nil
			},
		}).DialContext,
	},
	CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return nil
	},
}

// reservedNets are ranges that are global unicast and not "private" to the
// net package but still not the public internet: CGNAT (which holds tailnet
// hosts), the benchmark range and the IETF protocol block.
var reservedNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{"100.64.0.0/10", "198.18.0.0/15", "192.0.0.0/24", "169.254.0.0/16"} {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}()

// publicIP reports whether ip is an address a scraped photo URL may be
// fetched from. An IPv4-mapped IPv6 address is judged as the IPv4 it holds.
func publicIP(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, n := range reservedNets {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

// Ref names a person by platform and username (platform in upper case).
type Ref struct{ Platform, Username string }

type target struct{ id, url, oldImageID, oldSource string }

// Localize downloads the remote photo of each person in refs (every person
// of the profile when refs is nil) and points the person at the saved copy.
// The original URL is kept in profile_details.photo_source_url. A person
// whose photo cannot be fetched keeps the remote URL; the failures are
// returned, the rest still saved. It returns how many photos were saved.
func Localize(ctx context.Context, db *sql.DB, profileID string, refs []Ref) (int, []error) {
	if db == nil {
		return 0, nil
	}
	targets, err := load(ctx, db, profileID, refs)
	if err != nil {
		return 0, []error{err}
	}
	ctx, cancel := context.WithTimeout(vault.ContextWithProfileID(ctx, profileID), budget)
	defer cancel()

	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		saved int
		errs  []error
		sem   = make(chan struct{}, concurrency)
	)
	for _, t := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(t target) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					mu.Lock()
					errs = append(errs, fmt.Errorf("personphoto: %s: %v", hostOf(t.url), r))
					mu.Unlock()
				}
			}()
			err := save(ctx, db, profileID, t)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("personphoto: %s: %w", hostOf(t.url), err))
			} else {
				saved++
			}
		}(t)
	}
	wg.Wait()
	return saved, errs
}

func load(ctx context.Context, db *sql.DB, profileID string, refs []Ref) ([]target, error) {
	const cols = `SELECT id, image_url, COALESCE(CASE WHEN json_valid(profile_details) THEN json_extract(profile_details, '$.photo_image_id') END, ''),
		COALESCE(CASE WHEN json_valid(profile_details) THEN json_extract(profile_details, '$.photo_source_url') END, '')
		FROM people WHERE profile_id = ? AND (image_url LIKE 'http://%' OR image_url LIKE 'https://%')`
	query, args := cols, []interface{}{profileID}
	if refs != nil {
		if len(refs) == 0 {
			return nil, nil
		}
		conds := make([]string, len(refs))
		for i, r := range refs {
			conds[i] = "(platform = ? AND platform_username = ?)"
			args = append(args, r.Platform, r.Username)
		}
		query += " AND (" + strings.Join(conds, " OR ") + ")"
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("personphoto: listing people: %w", err)
	}
	defer rows.Close()
	var out []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.url, &t.oldImageID, &t.oldSource); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// restore points the person back at the photo already saved for it, if that
// image still exists.
func restore(ctx context.Context, db *sql.DB, profileID string, t target) bool {
	if t.oldImageID == "" {
		return false
	}
	if _, err := vault.GetImage(ctx, db, profileID, t.oldImageID); err != nil {
		return false
	}
	_, err := db.ExecContext(ctx, `UPDATE people SET image_url = ? WHERE id = ?`, "/vault-image/"+t.oldImageID, t.id)
	return err == nil
}

func save(ctx context.Context, db *sql.DB, profileID string, t target) error {
	// A re-read of an unchanged photo URL keeps the copy already saved.
	if t.oldSource == t.url && restore(ctx, db, profileID, t) {
		return nil
	}
	path, err := download(ctx, t.url)
	if err != nil {
		if restore(ctx, db, profileID, t) { // a failed fetch must not lose the saved photo
			return nil
		}
		return err
	}
	defer os.Remove(path)
	id, err := vault.Register(ctx, db, path, Source, "", "")
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE people SET image_url = ?, profile_details = json_set(
			CASE WHEN json_valid(profile_details) THEN profile_details ELSE '{}' END,
			'$.photo_source_url', ?, '$.photo_image_id', ?)
		WHERE id = ?`, "/vault-image/"+id, t.url, id, t.id)
	if err != nil {
		_ = vault.DeleteImage(ctx, db, profileID, id)
		return err
	}
	if t.oldImageID != "" && t.oldImageID != id {
		_ = vault.DeleteImage(ctx, db, profileID, t.oldImageID) // the photo this one replaces
	}
	return nil
}

func hostOf(raw string) string {
	if u, err := neturl.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return "photo"
}

// download fetches url into a temp file named for its image type.
func download(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "image/avif,image/webp,image/*,*/*;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxBytes {
		return "", errors.New("image over 10 MB")
	}
	ext, ok := map[string]string{
		"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif", "image/webp": ".webp",
	}[http.DetectContentType(body)]
	if !ok {
		return "", errors.New("not an image")
	}
	f, err := os.CreateTemp("", "person-photo-*"+ext)
	if err != nil {
		return "", err
	}
	_, werr := f.Write(body)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(f.Name())
		return "", werr
	}
	return filepath.Clean(f.Name()), nil
}

// Delete removes the saved photos of the given people, ahead of deleting the
// people themselves. Only a photo this package saved (its photo_image_id) is
// touched.
func Delete(ctx context.Context, db *sql.DB, profileID string, personIDs []string) {
	for _, id := range personIDs {
		var imageID string
		err := db.QueryRowContext(ctx, `SELECT COALESCE(CASE WHEN json_valid(profile_details) THEN json_extract(profile_details, '$.photo_image_id') END, '')
			FROM people WHERE id = ? AND profile_id = ?`, id, profileID).Scan(&imageID)
		if err != nil || imageID == "" {
			continue
		}
		if im, err := vault.GetImage(ctx, db, profileID, imageID); err == nil && im.Source == Source {
			_ = vault.DeleteImage(ctx, db, profileID, imageID)
		}
	}
}
