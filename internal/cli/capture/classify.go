package capturecli

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/capture"
	"github.com/monoes/mono-agent/internal/captureclassify"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/storage"
)

// Capture classification (Jev surface "capture", plan WS7). Off unless the
// capture's profile ran `monoagentcli jev enable capture`; then every capture
// the bridge writes gets a classification.json in the background, and a
// confident job posting, tender or person profile a suggested_route that
// `capture list --suggested` shows. Nothing is routed automatically.

// captureClassifyTimeout bounds one background classification.
const captureClassifyTimeout = 15 * time.Second

// captureOffCacheTTL is how long the hook remembers that the capture surface
// is off for a profile (or that there is no database), so a disabled surface
// costs at most one database open per profile per TTL.
const captureOffCacheTTL = 30 * time.Second

// captureClassifier is the after-write hook chained after the summarizer.
// It opens the database lazily, per capture, from dbPath: the installer has
// no DB of its own, and a bridge that never sees a capture never opens one.
type captureClassifier struct {
	dbPath  string
	logf    func(string, ...any)
	timeout time.Duration
	wg      sync.WaitGroup

	// open and now are seams for tests (nil ⇒ openProfileDB, time.Now).
	open func(string) (*storage.Database, error)
	now  func() time.Time

	mu sync.Mutex
	// offUntil maps a capture's profile (as recorded in the capture, "" for
	// none) to when its cached "surface off" answer expires. Only "off" is
	// cached: an enabled surface is re-checked on the DB it opens anyway, so
	// `jev disable capture` takes effect for the very next capture.
	offUntil map[string]time.Time
}

func newCaptureClassifier(logf func(string, ...any)) *captureClassifier {
	return &captureClassifier{dbPath: defaultDBPath, logf: logf, timeout: captureClassifyTimeout}
}

func (c *captureClassifier) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// knownOff reports whether profile was found off less than the TTL ago.
func (c *captureClassifier) knownOff(profile string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.offUntil[profile]
	if ok && c.clock().Before(until) {
		return true
	}
	if ok {
		delete(c.offUntil, profile)
	}
	return false
}

func (c *captureClassifier) rememberOff(profile string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.offUntil == nil {
		c.offUntil = map[string]time.Time{}
	}
	c.offUntil[profile] = c.clock().Add(captureOffCacheTTL)
}

// Handle starts the classification in the background and returns at once,
// so a capture is acknowledged exactly as fast as without it.
func (c *captureClassifier) Handle(res *capture.Result) {
	if c == nil || res == nil || res.Path == "" {
		return
	}
	dir, profile := res.Path, res.Meta.Profile
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.run(dir, profile)
	}()
}

// wait blocks until every started classification has finished (tests).
func (c *captureClassifier) wait() { c.wg.Wait() }

func (c *captureClassifier) run(dir, profile string) {
	defer func() {
		if r := recover(); r != nil {
			c.log("capture classification for %s panicked: %v", dir, r)
		}
	}()
	key := profile
	if c.knownOff(key) {
		return
	}
	open := c.open
	if open == nil {
		open = openProfileDB
	}
	// No database yet means no profile can have enabled the surface.
	db, err := open(c.dbPath)
	if err != nil {
		c.rememberOff(key)
		return
	}
	defer db.Close()
	profile = captureClassifyProfile(db.DB, profile)
	if !jevconf.Enabled(db.DB, profile, jevconf.Capture) {
		c.rememberOff(key)
		return
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = captureClassifyTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	r, err := classifyCaptureDir(ctx, db.DB, profile, dir)
	if err != nil {
		c.log("capture classification failed for %s: %v", dir, err)
		return
	}
	line := fmt.Sprintf("capture classified as %s (p=%.2f): %s", r.Kind, r.P, dir)
	if r.SuggestedRoute != "" {
		line += " — suggested route: " + r.SuggestedRoute
	}
	c.log("%s", line)
}

func (c *captureClassifier) log(format string, args ...any) {
	if c.logf != nil {
		c.logf(format, args...)
	}
}

// captureClassifyProfile is the capture's own profile, else the active one
// (settings active_profile_id, else "default") — how the CLI resolves it.
func captureClassifyProfile(db *sql.DB, profile string) string {
	if p := strings.TrimSpace(profile); p != "" {
		return p
	}
	var id string
	_ = db.QueryRow(`SELECT value FROM settings WHERE key = 'active_profile_id'`).Scan(&id)
	if id == "" {
		id = "default"
	}
	return id
}

// classifyCaptureDir classifies one envelope and writes classification.json.
func classifyCaptureDir(ctx context.Context, db *sql.DB, profile, dir string) (captureclassify.Result, error) {
	in, err := captureclassify.LoadInput(dir)
	if err != nil {
		return captureclassify.Result{}, err
	}
	client, err := jevconf.NewClient(ctx, db, profile, "", "", jevconf.Capture)
	if err != nil {
		return captureclassify.Result{}, err
	}
	in.Threshold = jevconf.Threshold(db, profile, jevconf.Capture, captureclassify.DefaultThreshold)
	r, err := captureclassify.Classify(ctx, client, in)
	if err != nil {
		return captureclassify.Result{}, err
	}
	if err := captureclassify.Write(dir, r); err != nil {
		return captureclassify.Result{}, fmt.Errorf("writing %s: %w", captureclassify.FileName, err)
	}
	return r, nil
}

func (d Deps) newCaptureClassifyCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "classify <path>",
		Short: "Classify a capture's page kind with Jev and write classification.json",
		Long: "Asks TypeSafe Jev what kind of page a capture is (job_posting, tender,\n" +
			"person_profile, article, docs, product, video or other) and writes the answer\n" +
			"into the capture directory as classification.json. A confident job posting or\n" +
			"tender gets suggested_route \"application\", a person profile \"person\"; nothing\n" +
			"is routed automatically.\n" +
			"\n" +
			"Sent to TypeSafe: the capture's URL, title and the first 6,000 characters of\n" +
			"its readable text. Needs the capture surface enabled for the profile\n" +
			"(`monoagentcli jev enable capture`), or --force for a one-off.",
		Example: "  monoagentcli capture classify ~/.monomind/inbox/2026-09-25T10-00-00Z-example-com\n" +
			"  monoagentcli capture classify <path> --force --json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := expandPath(args[0])
			meta, err := capture.ReadMeta(dir)
			if err != nil {
				return d.InvalidInput("%s is not a capture: %v", args[0], err)
			}
			explicit := strings.TrimSpace(d.ProfileID())
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			profile := d.ProfileID()
			if explicit == "" && strings.TrimSpace(meta.Profile) != "" {
				profile = strings.TrimSpace(meta.Profile)
			}
			if !force && !jevconf.Enabled(db.DB, profile, jevconf.Capture) {
				return d.InvalidInput("capture classification is off for profile %q: run `monoagentcli jev enable capture`"+
					" to turn it on, or pass --force to classify this one capture anyway", profile)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			r, err := classifyCaptureDir(ctx, db.DB, profile, dir)
			if err != nil {
				return fmt.Errorf("classifying capture: %w", err)
			}
			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(r)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Kind: %s (p=%.2f, model %s)\n", r.Kind, r.P, r.Model)
			if r.SuggestedRoute != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Suggested route: %s\n", r.SuggestedRoute)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Classify even though the capture surface is not enabled for the profile")
	return cmd
}

// suggestedCapture is a `capture list --suggested` row.
type suggestedCapture struct {
	capture.Entry
	Classification captureclassify.Result `json:"classification"`
}

// suggestedCaptures keeps the entries whose classification.json carries a
// suggested_route. An unreadable classification is skipped, not fatal.
func suggestedCaptures(entries []capture.Entry) []suggestedCapture {
	out := []suggestedCapture{}
	for _, e := range entries {
		r, ok, err := captureclassify.Read(e.Path)
		if err != nil || !ok || r.SuggestedRoute == "" {
			continue
		}
		out = append(out, suggestedCapture{Entry: e, Classification: r})
	}
	return out
}

// printSuggestedCaptures renders `capture list --suggested`.
func (d Deps) printSuggestedCaptures(w io.Writer, jsonOut bool, entries []capture.Entry) error {
	rows := suggestedCaptures(entries)
	if jsonOut {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "No captures with a suggested route.")
		return nil
	}
	table := d.NewTable(w, []string{"CAPTURED", "KIND", "P", "ROUTE", "TITLE", "PATH"})
	for _, r := range rows {
		_ = table.Append([]string{
			r.CapturedAt, r.Classification.Kind, fmt.Sprintf("%.2f", r.Classification.P),
			r.Classification.SuggestedRoute, truncateCaptureCell(r.Title, 40), r.Path,
		})
	}
	return table.Render()
}
