// Package arbeitnow implements discovery.Source against Arbeitnow's free,
// public, unauthenticated job-board API (https://www.arbeitnow.com/api/
// job-board-api) — verified live and checked against robots.txt (a plain
// "Disallow:" under "User-agent: *", i.e. nothing disallowed, no
// AI-crawler-specific restrictions) before being added here.
package arbeitnow

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/discovery"
)

// apiBase and robotsURL are vars, not consts, so tests can point them at
// an httptest server — matching internal/nodes/service/youtube.go's
// youtubeAPIBase convention.
var (
	apiBase   = "https://www.arbeitnow.com/api/job-board-api"
	robotsURL = "https://www.arbeitnow.com/robots.txt"
)

const (
	maxLimit = 100
	maxPages = 10 // safety cap: the API has no server-side keyword search (see Search's doc comment), so an obscure keyword could otherwise page forever
)

// Source queries Arbeitnow's public job-board API.
type Source struct{}

// New creates an Arbeitnow Source.
func New() *Source { return &Source{} }

// Name identifies results from this Source as coming from "arbeitnow".
func (s *Source) Name() string { return "arbeitnow" }

type apiResponse struct {
	Data  []apiJob `json:"data"`
	Links struct {
		Next *string `json:"next"`
	} `json:"links"`
}

type apiJob struct {
	Slug        string   `json:"slug"`
	CompanyName string   `json:"company_name"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Remote      bool     `json:"remote"`
	URL         string   `json:"url"`
	Tags        []string `json:"tags"`
	JobTypes    []string `json:"job_types"`
	Location    string   `json:"location"`
	CreatedAt   int64    `json:"created_at"`
}

// Search fetches Arbeitnow's job feed page by page and filters client-side
// by query.Keywords (matched case-insensitively against title and tags) —
// the API has no server-side search parameter (verified directly: a
// ?search= query returns the same unfiltered feed), only ?page= pagination.
// Stops once query.Limit matching results are collected or maxPages pages
// have been fetched, whichever comes first.
func (s *Source) Search(ctx context.Context, query discovery.SearchQuery) ([]discovery.Result, error) {
	limit := query.Limit
	if limit <= 0 || limit > maxLimit {
		limit = maxLimit
	}

	if err := discovery.CheckRobotsAllowed(ctx, robotsURL, "/api/job-board-api"); err != nil {
		return nil, fmt.Errorf("arbeitnow: %w", err)
	}

	keywords := strings.ToLower(strings.TrimSpace(query.Keywords))
	location := strings.ToLower(strings.TrimSpace(query.Location))

	var results []discovery.Result
	pageURL := apiBase
	for page := 1; page <= maxPages && pageURL != "" && len(results) < limit; page++ {
		resp, err := fetchPage(ctx, pageURL)
		if err != nil {
			if len(results) > 0 {
				return results, fmt.Errorf("arbeitnow: search truncated after %d result(s): %w", len(results), err)
			}
			return nil, fmt.Errorf("arbeitnow: %w", err)
		}

		for _, j := range resp.Data {
			if keywords != "" && !matchesKeywords(j, keywords) {
				continue
			}
			if location != "" && !strings.Contains(strings.ToLower(j.Location), location) {
				continue
			}
			results = append(results, toResult(j))
			if len(results) >= limit {
				break
			}
		}

		if resp.Links.Next == nil || len(results) >= limit {
			break
		}
		pageURL = *resp.Links.Next

		// Paced like linkedin's Source.Search -- confirmed live that fetching
		// pages back-to-back with no delay gets a 429 from Arbeitnow within a
		// handful of requests.
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		case <-time.After(paceDelay()):
		}
	}

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// paceDelay mirrors internal/discovery/sources/linkedin's own pacing —
// 1.5-3.0s between requests.
func paceDelay() time.Duration {
	return time.Duration(1500+rand.Intn(1500)) * time.Millisecond
}

func matchesKeywords(j apiJob, keywords string) bool {
	if strings.Contains(strings.ToLower(j.Title), keywords) {
		return true
	}
	for _, tag := range j.Tags {
		if strings.Contains(strings.ToLower(tag), keywords) {
			return true
		}
	}
	return false
}

func toResult(j apiJob) discovery.Result {
	jobType := ""
	if len(j.JobTypes) > 0 {
		jobType = strings.Join(j.JobTypes, ", ")
	}
	postedAt := ""
	if j.CreatedAt > 0 {
		postedAt = time.Unix(j.CreatedAt, 0).UTC().Format(time.RFC3339)
	}
	return discovery.Result{
		Title:       j.Title,
		Company:     j.CompanyName,
		URL:         j.URL,
		Location:    j.Location,
		Description: j.Description,
		JobType:     jobType,
		IsRemote:    j.Remote,
		PostedAt:    postedAt,
	}
}

func fetchPage(ctx context.Context, pageURL string) (*apiResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; MonoAgent/1.0)")
	resp, err := discovery.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", pageURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s: status %d", pageURL, resp.StatusCode)
	}
	var out apiResponse
	if err := json.NewDecoder(discovery.LimitBody(resp.Body, discovery.MaxAPIBytes)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding response from %s: %w", pageURL, err)
	}
	return &out, nil
}
