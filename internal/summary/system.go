package summary

import (
	"context"
	"database/sql"
	"time"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
)

// AutomationSource is what the automations section reads; cmd wires the
// real registry and selector-health table (as `automation doctor` does).
type AutomationSource interface {
	List() ([]automation.InstalledInfo, error)
	SelectorHealth() ([]automation.SelectorHealth, error)
	// DeclaredKeys reports the selector keys the installed package declares;
	// ok is false when unknown (a health row is then not called stale).
	DeclaredKeys(automationID string) (keys map[string]bool, ok bool)
}

type DaemonStatus struct {
	Running        bool   `json:"running"`
	PID            int    `json:"pid"`
	APIAddr        string `json:"api_addr"`
	BridgeAddr     string `json:"bridge_addr"`
	Version        string `json:"version"`
	HeartbeatAgeMS int64  `json:"heartbeat_age_ms"`
}

type BridgeStatus struct {
	Status    string `json:"status"` // connected | waiting | unpaired
	Connected bool   `json:"connected"`
	Addr      string `json:"addr"`
	InFlight  int    `json:"in_flight"`
	Version   string `json:"version"`
}

type OrgServeStatus struct {
	Running bool     `json:"running"`
	Orgs    []string `json:"orgs"`
}

type ServicesSection struct {
	Daemon   DaemonStatus   `json:"daemon"`
	Bridge   *BridgeStatus  `json:"bridge"` // null when no bridge answers
	OrgServe OrgServeStatus `json:"org_serve"`
	Error    string         `json:"error,omitempty"`
}

type SelectorCounts struct {
	OK       int `json:"ok"`
	Decaying int `json:"decaying"`
	Broken   int `json:"broken"`
	Stale    int `json:"stale"`
}

type SelectorRef struct {
	AutomationID string `json:"automation_id"`
	SelectorKey  string `json:"selector_key"`
}

type AutomationsSection struct {
	Installed      int            `json:"installed"`
	Enabled        int            `json:"enabled"`
	Unavailable    int            `json:"unavailable"`
	PendingUpdate  int            `json:"pending_update"`
	ScriptsBlocked int            `json:"scripts_blocked"`
	Selectors      SelectorCounts `json:"selectors"`
	Broken         []SelectorRef  `json:"broken"`
	Error          string         `json:"error,omitempty"`
}

type RecordingsSection struct {
	Total           int    `json:"total"`
	Unsaved         int    `json:"unsaved"`    // complete, not yet saved into an automation
	Incomplete      int    `json:"incomplete"` // no stop frame
	LatestStartedAt string `json:"latest_started_at"`
	Error           string `json:"error,omitempty"`
}

type JevSection struct {
	// KeyConfigured and KeySource are about Provider's key ("keyless" for a
	// provider that needs none).
	KeyConfigured   bool    `json:"key_configured"`
	KeySource       string  `json:"key_source"`
	SurfacesEnabled int     `json:"surfaces_enabled"`
	Calls24h        int     `json:"calls_24h"`
	Failures24h     int     `json:"failures_24h"`
	EstimatedUSD24h float64 `json:"estimated_usd_24h"`
	// Provider is the machine's default decision provider ("typesafe" unless chosen).
	Provider string `json:"provider"`
	Error    string `json:"error,omitempty"`
}

type SessionRow struct {
	Platform string `json:"platform"`
	Username string `json:"username"`
	Expiry   string `json:"expiry"`
	Status   string `json:"status"` // active | expiring | expired
	// ProfileID and ProfileName are set only in the All profiles view
	// (Merge), so every row says whose it is.
	ProfileID   string `json:"profile_id,omitempty"`
	ProfileName string `json:"profile_name,omitempty"`
}

type AccountsSection struct {
	Sessions     []SessionRow `json:"sessions"`
	Active       int          `json:"active"` // includes expiring
	Expired      int          `json:"expired"`
	ExpiringSoon int          `json:"expiring_soon"`
	Error        string       `json:"error,omitempty"`
}

// VaultSection is counts only: never a secret's name, username, URL, kind
// or value — the dashboard shows how many, not which.
type VaultSection struct {
	Secrets    int    `json:"secrets"`
	Images     int    `json:"images"`
	ImageBytes int64  `json:"image_bytes"`
	Error      string `json:"error,omitempty"`
}

// ExpiringWindow is how close to expiry a login counts as "expiring".
const ExpiringWindow = 72 * time.Hour

func servicesSection(o Options) *ServicesSection {
	s := &ServicesSection{OrgServe: OrgServeStatus{Orgs: []string{}}}
	if o.Daemon != nil {
		if d := o.Daemon(); d != nil {
			s.Daemon = *d
		}
	}
	if o.Bridge != nil {
		if b, err := o.Bridge(); err == nil {
			s.Bridge = b
		}
	}
	if o.OrgServe != nil {
		running, orgs := o.OrgServe()
		s.OrgServe.Running = running
		if orgs != nil {
			s.OrgServe.Orgs = orgs
		}
	}
	return s
}

func automationsSection(o Options) *AutomationsSection {
	s := &AutomationsSection{Broken: []SelectorRef{}}
	if o.Automations == nil {
		s.Error = "automation registry unavailable"
		return s
	}
	infos, err := o.Automations.List()
	if err != nil {
		s.Error = err.Error()
		return s
	}
	installed := map[string]bool{}
	for _, i := range infos {
		installed[i.ID] = true
		s.Installed++
		if i.Enabled {
			s.Enabled++
		}
		if !i.Available {
			s.Unavailable++
		}
		if i.PendingUpdate != "" {
			s.PendingUpdate++
		}
		if i.ContainsScripts && !i.ScriptsAllowed {
			s.ScriptsBlocked++
		}
	}
	health, err := o.Automations.SelectorHealth()
	if err != nil {
		s.Error = err.Error()
		return s
	}
	declared := map[string]map[string]bool{}
	seen := map[string]map[string]bool{}
	for _, h := range health {
		if seen[h.AutomationID] == nil {
			seen[h.AutomationID] = map[string]bool{}
		}
		seen[h.AutomationID][h.Key] = true
	}
	for _, h := range health {
		if !installed[h.AutomationID] {
			continue // rows of uninstalled packages are not this profile's problem
		}
		keys, seen := declared[h.AutomationID]
		if !seen {
			if k, ok := o.Automations.DeclaredKeys(h.AutomationID); ok {
				keys = k
			}
			declared[h.AutomationID] = keys
		}
		status := h.Status
		if status == "" {
			status = automation.SelectorStatus(h)
		}
		if keys != nil && !keys[h.Key] {
			status = automation.HealthStale
		}
		switch status {
		case automation.HealthOK:
			s.Selectors.OK++
		case automation.HealthDecaying:
			s.Selectors.Decaying++
		case automation.HealthBroken:
			s.Selectors.Broken++
			s.Broken = append(s.Broken, SelectorRef{AutomationID: h.AutomationID, SelectorKey: h.Key})
		case automation.HealthStale:
			s.Selectors.Stale++
		}
	}
	// Declared selectors with no runs yet are ok, as in `automation doctor`.
	for _, i := range infos {
		keys, ok := declared[i.ID]
		if !ok {
			keys, _ = o.Automations.DeclaredKeys(i.ID)
		}
		for k := range keys {
			if !seen[i.ID][k] {
				s.Selectors.OK++
			}
		}
	}
	return s
}

func recordingsSection(o Options) *RecordingsSection {
	s := &RecordingsSection{}
	if o.Recordings == nil {
		s.Error = "recording store unavailable"
		return s
	}
	list, err := o.Recordings()
	if err != nil {
		s.Error = err.Error()
		return s
	}
	for _, r := range list {
		s.Total++
		if !r.Complete {
			s.Incomplete++
		} else if r.Automation == "" {
			s.Unsaved++
		}
		if r.StartedAt > s.LatestStartedAt { // RFC3339 sorts lexically
			s.LatestStartedAt = r.StartedAt
		}
	}
	return s
}

// jevSection reads configuration and the local usage table only — no call
// to TypeSafe, not even a key test.
func jevSection(ctx context.Context, o Options) *JevSection {
	s := &JevSection{Provider: jevconf.DefaultProvider(o.DB)}
	if o.DB == nil {
		s.Error = noDB
		return s
	}
	// The key of the provider shown (the machine default), listed, never read.
	if src, err := jevconf.ProviderKeySourceOf(ctx, o.DB, o.ProfileID, s.Provider); err == nil {
		s.KeyConfigured, s.KeySource = true, src
	}
	for _, surf := range jevconf.Surfaces {
		if jevconf.Enabled(o.DB, o.ProfileID, surf) {
			s.SurfacesEnabled++
		}
	}
	usage, err := jevconf.UsageSince(o.DB, o.ProfileID, o.Now.Add(-24*time.Hour))
	if err != nil {
		s.Error = err.Error()
		return s
	}
	for _, u := range usage {
		s.Calls24h += u.Calls
		s.Failures24h += u.Failures
		s.EstimatedUSD24h += u.EstimatedUSD
	}
	return s
}

func accountsSection(ctx context.Context, o Options) *AccountsSection {
	s := &AccountsSection{Sessions: []SessionRow{}}
	if o.DB == nil {
		s.Error = noDB
		return s
	}
	rows, err := o.DB.QueryContext(ctx, `SELECT platform, username, expiry FROM crawler_sessions
		WHERE profile_id = ? AND platform <> '' ORDER BY platform, username`, o.ProfileID)
	if err != nil {
		s.Error = err.Error()
		return s
	}
	defer rows.Close()
	for rows.Next() {
		var r SessionRow
		var expiry time.Time
		if err := rows.Scan(&r.Platform, &r.Username, &expiry); err != nil {
			s.Error = err.Error()
			return s
		}
		r.Username = automation.DisplayUsername(r.Username)
		r.Expiry = expiry.UTC().Format(time.RFC3339)
		switch {
		case expiry.Before(o.Now):
			r.Status = "expired"
			s.Expired++
		case expiry.Before(o.Now.Add(ExpiringWindow)):
			r.Status = "expiring"
			s.Active++
			s.ExpiringSoon++
		default:
			r.Status = "active"
			s.Active++
		}
		s.Sessions = append(s.Sessions, r)
	}
	s.Error = errString(rows.Err())
	return s
}

func vaultSection(ctx context.Context, o Options) *VaultSection {
	s := &VaultSection{}
	if o.DB == nil {
		s.Error = noDB
		return s
	}
	var bytes sql.NullInt64
	err := o.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM vault_secrets WHERE profile_id = ?`, o.ProfileID).Scan(&s.Secrets)
	if err == nil {
		err = o.DB.QueryRowContext(ctx, `SELECT COUNT(*), SUM(size_bytes) FROM vault_images WHERE profile_id = ?`,
			o.ProfileID).Scan(&s.Images, &bytes)
	}
	s.ImageBytes = bytes.Int64
	s.Error = errString(err)
	return s
}
