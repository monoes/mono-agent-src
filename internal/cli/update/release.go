package update

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/release"
)

// LatestInfo is the latest release as the updaters need it: from the verified
// signed manifest, or, only while release.AllowLegacyGitHubUpdates, from the
// GitHub API.
type LatestInfo struct {
	Tag      string
	URL      string
	Manifest *release.Manifest // set on the signed path
	Legacy   *LatestRelease    // set on the legacy path
}

// FetchLatest prefers the signed manifest (rc). Only "unavailable" (no pinned key,
// manifest not reachable) falls back to GitHub's latest-release endpoint
// (legacyURL); a manifest that fails a check is an error, never a reason to
// try the weaker path.
func FetchLatest(ctx context.Context, rc *release.Client, legacyURL string, force bool) (*LatestInfo, error) {
	info, err := fetchLatest(ctx, rc, legacyURL, force)
	if err != nil && isNetworkErr(err) {
		return nil, &UnreachableError{Err: err}
	}
	return info, err
}

// UnreachableError is a latest-release lookup that failed at the network
// level (offline, DNS, refused, timeout). Error() is the actionable text;
// Err keeps the raw cause for --verbose and the --json detail field.
type UnreachableError struct{ Err error }

func (e *UnreachableError) Error() string {
	return "could not reach the update server; check your connection"
}
func (e *UnreachableError) Unwrap() error { return e.Err }

func isNetworkErr(err error) bool {
	var ne net.Error
	var dns *net.DNSError
	return errors.As(err, &ne) || errors.As(err, &dns)
}

// withDetail appends the raw cause of an UnreachableError when -v is set.
func withDetail(cmd *cobra.Command, err error) error {
	var ue *UnreachableError
	if v, _ := cmd.Flags().GetBool("verbose"); v && errors.As(err, &ue) {
		return fmt.Errorf("%s (%v)", ue.Error(), ue.Err)
	}
	return err
}

func fetchLatest(ctx context.Context, rc *release.Client, legacyURL string, force bool) (*LatestInfo, error) {
	rc.Force = rc.Force || force
	m, err := rc.Latest(ctx)
	if err == nil {
		return &LatestInfo{Tag: m.Version, URL: m.NotesURL, Manifest: m}, nil
	}
	if !errors.Is(err, release.ErrUnavailable) {
		return nil, fmt.Errorf("the signed release manifest was rejected: %w; nothing was installed", err)
	}
	if !release.AllowLegacyGitHubUpdates {
		return nil, fmt.Errorf("cannot verify an update: %w", err)
	}
	rel, lerr := FetchLatestRelease(ctx, legacyURL)
	if lerr != nil {
		return nil, lerr
	}
	return &LatestInfo{Tag: rel.TagName, URL: rel.HTMLURL, Legacy: rel}, nil
}
