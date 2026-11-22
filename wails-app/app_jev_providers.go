package main

// Decision providers (Settings → Jev → "Decision model"). Like app_jev.go,
// every method shells out to `monoagentcli jev provider …` through
// runMonoCLI: no vault, no settings table, no SQL here. A provider's API key
// travels only on the CLI's stdin — never argv, never a log line.

import (
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// JevProviderCaps is what a provider can answer and how it gets its
// probabilities (native | logprob | softmax | sampled | none).
// MaxChoiceOptions and QuestionsPerRequest are 0 for no limit;
// OptionsPerRequest, when set, is how many options one request carries (a
// larger choice is split).
type JevProviderCaps struct {
	Kinds               []string `json:"kinds"`
	Probability         string   `json:"probability"`
	MaxChoiceOptions    int      `json:"max_choice_options"`
	OptionsPerRequest   int      `json:"options_per_request,omitempty"`
	QuestionsPerRequest int      `json:"questions_per_request"`
	Models              bool     `json:"models"`
}

// JevAddProbe is the probe `jev provider add` runs after saving.
type JevAddProbe struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// JevProviderInfo is one provider record (never its key, only key_set).
// Probe is set only on the result of JevAddProvider.
type JevProviderInfo struct {
	Name    string          `json:"name"`
	Kind    string          `json:"kind"`
	BaseURL string          `json:"base_url"`
	Model   string          `json:"model"`
	KeySet  bool            `json:"key_set"`
	Builtin bool            `json:"builtin"`
	Caps    JevProviderCaps `json:"caps"`
	Probe   *JevAddProbe    `json:"probe,omitempty"`
}

// JevProviderList mirrors `jev provider list --json`.
type JevProviderList struct {
	Default   string            `json:"default"`
	Providers []JevProviderInfo `json:"providers"`
	Kinds     []string          `json:"kinds"`
}

// JevAddProviderRequest is the GUI's add-provider form. APIKey is optional
// (keyless local servers); prices <= 0 are left to the CLI's default.
// Replace passes --yes: the GUI showed its own confirm that the provider of
// that name is replaced (its host, kind or key may change), the consent
// `jev provider add` asks for in a terminal.
type JevAddProviderRequest struct {
	Name          string  `json:"name"`
	Kind          string  `json:"kind"`
	BaseURL       string  `json:"base_url"`
	Model         string  `json:"model"`
	APIKey        string  `json:"api_key"`
	USDPerMTokIn  float64 `json:"usd_per_mtok_in"`
	USDPerMTokOut float64 `json:"usd_per_mtok_out"`
	NoProbe       bool    `json:"no_probe"`
	Replace       bool    `json:"replace"`
}

// JevUseResult mirrors `jev provider use --json`.
type JevUseResult struct {
	Scope    string `json:"scope"` // default | surface
	Surface  string `json:"surface"`
	Provider string `json:"provider"`
	Cleared  bool   `json:"cleared"`
}

// JevProbeResult mirrors `jev provider probe --json` (exit 0 even when ok=false).
type JevProbeResult struct {
	OK        bool            `json:"ok"`
	Caps      JevProviderCaps `json:"caps"`
	LatencyMS int             `json:"latency_ms"`
	Error     string          `json:"error"`
}

// JevRemoveProviderResult mirrors `jev provider remove --json`.
type JevRemoveProviderResult struct {
	Removed    bool   `json:"removed"`
	Name       string `json:"name"`
	KeyRemoved bool   `json:"key_removed"`
}

var (
	jevProviderName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	jevSurfaceName  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
)

func jevName(what, v string, re *regexp.Regexp) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", errors.New(what + " is required")
	}
	if !re.MatchString(v) {
		return "", errors.New("invalid " + what + " " + strconv.Quote(v))
	}
	return v, nil
}

// jevBaseURL accepts an http(s) URL with a host and no credentials. Whether
// plain http is allowed (loopback only) is the CLI's rule.
func jevBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("base URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return "", errors.New("invalid base URL " + strconv.Quote(raw) + ": use http(s)://host/…")
	}
	return raw, nil
}

func jevModel(m string) (string, error) {
	m = strings.TrimSpace(m)
	if strings.HasPrefix(m, "-") || strings.IndexFunc(m, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", errors.New("invalid model " + strconv.Quote(m))
	}
	return m, nil
}

// JevProviders lists the saved decision providers, the default and the
// kinds this CLI can add.
func (a *App) JevProviders() (JevProviderList, error) {
	if err := a.signedOut(); err != nil {
		return JevProviderList{}, err
	}
	var l JevProviderList
	if err := a.runMonoCLI("", &l, "jev", "provider", "list"); err != nil {
		return JevProviderList{}, err
	}
	if l.Providers == nil {
		l.Providers = []JevProviderInfo{}
	}
	if l.Kinds == nil {
		l.Kinds = []string{}
	}
	return l, nil
}

// JevAddProvider saves a provider; the CLI probes it unless NoProbe. The key
// (optional) goes to the CLI on stdin only.
func (a *App) JevAddProvider(req JevAddProviderRequest) (JevProviderInfo, error) {
	if err := a.signedOut(); err != nil {
		return JevProviderInfo{}, err
	}
	name, err := jevName("provider name", req.Name, jevProviderName)
	if err != nil {
		return JevProviderInfo{}, err
	}
	kind, err := jevName("kind", req.Kind, jevProviderName)
	if err != nil {
		return JevProviderInfo{}, err
	}
	base, err := jevBaseURL(req.BaseURL)
	if err != nil {
		return JevProviderInfo{}, err
	}
	model, err := jevModel(req.Model)
	if err != nil {
		return JevProviderInfo{}, err
	}
	key := strings.TrimSpace(req.APIKey)
	if strings.ContainsAny(key, "\r\n") {
		return JevProviderInfo{}, errors.New("the API key must be one line")
	}
	if req.USDPerMTokIn < 0 || req.USDPerMTokOut < 0 {
		return JevProviderInfo{}, errors.New("prices cannot be negative")
	}
	list, err := a.JevProviders()
	if err != nil {
		return JevProviderInfo{}, err
	}
	known := false
	for _, k := range list.Kinds {
		known = known || k == kind
	}
	if !known {
		return JevProviderInfo{}, errors.New("unknown provider kind " + strconv.Quote(kind) + "; this CLI adds: " + strings.Join(list.Kinds, ", "))
	}

	args := []string{"jev", "provider", "add", name, "--kind", kind, "--base-url=" + base}
	if model != "" {
		args = append(args, "--model="+model)
	}
	if key != "" {
		args = append(args, "--key-stdin")
	}
	if req.USDPerMTokIn > 0 {
		args = append(args, "--usd-per-mtok-in", strconv.FormatFloat(req.USDPerMTokIn, 'f', -1, 64))
	}
	if req.USDPerMTokOut > 0 {
		args = append(args, "--usd-per-mtok-out", strconv.FormatFloat(req.USDPerMTokOut, 'f', -1, 64))
	}
	if req.NoProbe {
		args = append(args, "--no-probe")
	}
	if req.Replace {
		args = append(args, "--yes")
	}
	var p JevProviderInfo
	if err := a.runMonoCLI(key, &p, args...); err != nil {
		return JevProviderInfo{}, err
	}
	return p, nil
}

// JevUseProvider makes name the machine default (surface "") or one
// surface's provider. name "" with a surface clears that surface's choice so
// it follows the default again. It passes --yes: the GUI lists what each
// enabled feature sends and where, and asks, before calling this — the same
// consent `jev provider use` asks for in a terminal.
func (a *App) JevUseProvider(name, surface string) (JevUseResult, error) {
	if err := a.signedOut(); err != nil {
		return JevUseResult{}, err
	}
	var s string
	if strings.TrimSpace(surface) != "" {
		var err error
		if s, err = jevName("surface", surface, jevSurfaceName); err != nil {
			return JevUseResult{}, err
		}
	}
	var args []string
	if strings.TrimSpace(name) == "" && s != "" {
		args = []string{"jev", "provider", "use", "--surface", s, "--clear", "--yes"}
	} else {
		n, err := jevName("provider name", name, jevProviderName)
		if err != nil {
			return JevUseResult{}, err
		}
		args = []string{"jev", "provider", "use", n}
		if s != "" {
			args = append(args, "--surface", s)
		}
		args = append(args, "--yes")
	}
	var res JevUseResult
	if err := a.runMonoCLI("", &res, args...); err != nil {
		return JevUseResult{}, err
	}
	return res, nil
}

// JevProbeProvider asks the provider (through the CLI) what it can answer.
func (a *App) JevProbeProvider(name string) (JevProbeResult, error) {
	if err := a.signedOut(); err != nil {
		return JevProbeResult{}, err
	}
	n, err := jevName("provider name", name, jevProviderName)
	if err != nil {
		return JevProbeResult{}, err
	}
	var res JevProbeResult
	if err := a.runMonoCLI("", &res, "jev", "provider", "probe", n); err != nil {
		return JevProbeResult{}, err
	}
	return res, nil
}

// JevRemoveProvider deletes a provider and its key. The CLI refuses the
// built-in one and any provider still in use.
func (a *App) JevRemoveProvider(name string) (JevRemoveProviderResult, error) {
	if err := a.signedOut(); err != nil {
		return JevRemoveProviderResult{}, err
	}
	n, err := jevName("provider name", name, jevProviderName)
	if err != nil {
		return JevRemoveProviderResult{}, err
	}
	var res JevRemoveProviderResult
	if err := a.runMonoCLI("", &res, "jev", "provider", "remove", n); err != nil {
		return JevRemoveProviderResult{}, err
	}
	return res, nil
}
