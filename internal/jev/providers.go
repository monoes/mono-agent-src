package jev

import "strings"

// Protocols a decision provider speaks. Only ProtocolSystemOne has an adapter:
// the others are listed so Settings shows the whole field, and `jev provider
// use` refuses them until their adapter exists.
const (
	ProtocolSystemOne = "systemone" // POST /v1/systemone, the Jev request format
	ProtocolPending   = "pending"   // a different wire format, no adapter yet
)

// ProviderTypeSafe is the provider every install starts on.
const ProviderTypeSafe = "typesafe"

// ProviderCustom is the local or self-hosted /v1/systemone endpoint.
const ProviderCustom = "custom"

// Provider is one decision-model vendor (never a text generator, see AGENTS.md
// "TypeSafe Jev (decisions only)").
type Provider struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Protocol string   `json:"protocol"`
	Models   []string `json:"models"`
	// BaseURL is the default host; "" means the user must set one (the
	// catalog does not guess vendor endpoints).
	BaseURL string `json:"base_url"`
	// KeyOptional is true for a local endpoint that needs no key.
	KeyOptional bool   `json:"key_optional"`
	Note        string `json:"note"`
}

// Supported reports whether a client can talk to the provider today.
func (p Provider) Supported() bool { return p.Protocol == ProtocolSystemOne }

// Catalog lists the decision providers in display order.
var Catalog = []Provider{
	{ID: ProviderTypeSafe, Name: "TypeSafe", Protocol: ProtocolSystemOne, BaseURL: "https://api.typesafe.ai",
		Models: []string{DefaultModel}, Note: "Jev, via /v1/systemone."},
	{ID: ProviderCustom, Name: "Local / custom endpoint", Protocol: ProtocolSystemOne, BaseURL: "http://127.0.0.1:8080",
		KeyOptional: true, Note: "Any /v1/systemone server: a local model or a self-hosted one."},
	{ID: "liquid", Name: "Liquid AI", Protocol: ProtocolSystemOne,
		Models: []string{"d1", "d1-ultra", "d1-3B", "d1-omni-600M"}, Note: "d1 family, /v1/systemone compatible. Set the base URL."},
	{ID: "upstage", Name: "Upstage", Protocol: ProtocolSystemOne,
		Models: []string{"solar-decide", "solar-decide-flash"}, Note: "Solar Decide, Jev request format. Set the base URL."},
	{ID: "openai", Name: "OpenAI", Protocol: ProtocolPending, Models: []string{"gpt-6-luna"}, Note: "Decisions API, POST /v1/decisions."},
	{ID: "google", Name: "Google", Protocol: ProtocolPending, Models: []string{"gemini-decision-1"}, Note: "Gemini Decisions API (batch)."},
	{ID: "perplexity", Name: "Perplexity", Protocol: ProtocolPending, Models: []string{"pplx-decider-v1.1", "pplx-decider-v1.2"}, Note: "Routing and intent classification."},
	{ID: "parallel", Name: "Parallel", Protocol: ProtocolPending, Models: []string{"decisions-fast"}, Note: "Web-grounded decisions, up to 100 typed questions per call."},
	{ID: "cloudflare", Name: "Cloudflare", Protocol: ProtocolPending, Models: []string{"clef", "clef-flash"}, Note: "Edge routing and moderation."},
	{ID: "inception", Name: "Inception", Protocol: ProtocolPending, Models: []string{"mercury-decide"}, Note: "High-throughput decision loops."},
	{ID: "respan", Name: "Respan", Protocol: ProtocolPending, Models: []string{"span-01", "span-01-lite"}, Note: "Trace guardrails: prompt injection, hallucination, agent loops."},
	{ID: "baseten", Name: "Baseten", Protocol: ProtocolPending, Models: []string{"groundhog-1", "bison-1"}, Note: "Open-weight decision models."},
	{ID: "morph", Name: "Morph", Protocol: ProtocolPending, Models: []string{"reflex"}, Note: "Budget decision API."},
	{ID: "telnyx", Name: "Telnyx", Protocol: ProtocolPending, Models: []string{"telnyx-decision"}, Note: "Voice and real-time communications."},
	{ID: "tazo", Name: "Tazo Labs", Protocol: ProtocolPending, Models: []string{"tazo"}, Note: "Audio-native turn-taking."},
}

// ProviderByID finds a catalog entry by id (case-insensitive).
func ProviderByID(id string) (Provider, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, p := range Catalog {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}
