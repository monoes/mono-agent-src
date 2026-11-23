// Package jobicy implements discovery.Source against Jobicy's free, public,
// unauthenticated remote-jobs API (https://jobicy.com/api/v2/remote-jobs) —
// verified live and checked against robots.txt, which explicitly welcomes
// AI crawlers ("Content-Signal: ai-train=yes, search=yes, ai-input=yes")
// and does not disallow the API path.
package jobicy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/monoes/mono-agent/internal/discovery"
)

// apiBase and robotsURL are vars, not consts, so tests can point them at
// an httptest server — matching internal/nodes/service/youtube.go's
// youtubeAPIBase convention.
var (
	apiBase   = "https://jobicy.com/api/v2/remote-jobs"
	robotsURL = "https://jobicy.com/robots.txt"
)

const maxLimit = 100

// Source queries Jobicy's public remote-jobs API.
type Source struct{}

// New creates a Jobicy Source.
func New() *Source { return &Source{} }

// Name identifies results from this Source as coming from "jobicy".
func (s *Source) Name() string { return "jobicy" }

type apiResponse struct {
	Jobs []apiJob `json:"jobs"`
}

type apiJob struct {
	URL            string   `json:"url"`
	JobTitle       string   `json:"jobTitle"`
	CompanyName    string   `json:"companyName"`
	JobIndustry    []string `json:"jobIndustry"`
	JobType        []string `json:"jobType"`
	JobGeo         string   `json:"jobGeo"`
	PubDate        string   `json:"pubDate"`
	JobExcerpt     string   `json:"jobExcerpt"`
	JobDescription string   `json:"jobDescription"`
}

// Search queries Jobicy's remote-jobs API, which supports real server-side
// filtering: ?tag= for a keyword and ?geo= for a location (both verified
// directly against the live API — unlike Arbeitnow's feed, which has no
// server-side search). Jobicy is remote-jobs-only, so every result is
// IsRemote=true.
func (s *Source) Search(ctx context.Context, query discovery.SearchQuery) ([]discovery.Result, error) {
	limit := query.Limit
	if limit <= 0 || limit > maxLimit {
		limit = maxLimit
	}

	if err := discovery.CheckRobotsAllowed(ctx, robotsURL, "/api/v2/remote-jobs"); err != nil {
		return nil, fmt.Errorf("jobicy: %w", err)
	}

	params := url.Values{}
	params.Set("count", strconv.Itoa(limit))
	if query.Keywords != "" {
		params.Set("tag", query.Keywords)
	}
	if query.Location != "" {
		params.Set("geo", query.Location)
	}

	out, err := fetch(ctx, params)
	if err != nil {
		// Jobicy's geo filter only accepts a small set of predefined
		// region/country slugs (e.g. "germany", "europe"), not arbitrary
		// city text — a free-text location like "Berlin" (valid for other
		// sources) always 400s here. Since Jobicy is remote-jobs-only
		// anyway, drop the geo filter and retry rather than losing every
		// result over a filter mismatch.
		if isInvalidGeoError(err) && query.Location != "" {
			params.Del("geo")
			out, err = fetch(ctx, params)
		}
		if err != nil {
			return nil, err
		}
	}

	results := make([]discovery.Result, 0, len(out.Jobs))
	for _, j := range out.Jobs {
		if len(results) >= limit {
			break
		}
		results = append(results, discovery.Result{
			Title:       j.JobTitle,
			Company:     j.CompanyName,
			URL:         j.URL,
			Location:    j.JobGeo,
			Description: firstNonEmpty(j.JobDescription, j.JobExcerpt),
			JobType:     strings.Join(j.JobType, ", "),
			IsRemote:    true,
			PostedAt:    j.PubDate,
		})
	}
	return results, nil
}

// invalidGeoError wraps a 400 response whose body indicates an unrecognized
// geo slug, so Search can distinguish it from other failures and retry.
type invalidGeoError struct{ status int }

func (e *invalidGeoError) Error() string {
	return fmt.Sprintf("jobicy: status %d: invalid geo value", e.status)
}

func isInvalidGeoError(err error) bool {
	_, ok := err.(*invalidGeoError)
	return ok
}

// fetch issues one request against apiBase with the given query params and
// decodes the JSON response.
func fetch(ctx context.Context, params url.Values) (apiResponse, error) {
	var out apiResponse
	pageURL := apiBase + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return out, fmt.Errorf("jobicy: building request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; MonoAgent/1.0)")
	resp, err := discovery.Client.Do(req)
	if err != nil {
		return out, fmt.Errorf("jobicy: fetching %s: %w", pageURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == http.StatusBadRequest && strings.Contains(string(body), "'geo' value") {
			return out, &invalidGeoError{status: resp.StatusCode}
		}
		return out, fmt.Errorf("jobicy: fetching %s: status %d", pageURL, resp.StatusCode)
	}
	if err := json.NewDecoder(discovery.LimitBody(resp.Body, discovery.MaxAPIBytes)).Decode(&out); err != nil {
		return out, fmt.Errorf("jobicy: decoding response: %w", err)
	}
	return out, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
