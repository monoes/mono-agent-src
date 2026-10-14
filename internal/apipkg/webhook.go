package apipkg

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// Webhook is a package's app-webhook registration (FORMAT.md §17): the
// requests that create, check and delete the vendor's webhook when a
// workflow with the app's trigger is activated and deactivated, written
// in the package's template language and sent with its credential
// injection (OAuth2/OAuth1/basic/SigV4/headers alike).
//
// Templates see p (the trigger's register config with the fields'
// defaults), cred, hook (url, path, secret, id, workflow_id, node_id,
// extra), pre/pres (earlier create steps) and el (for_each). id and extra
// also see res (the last create response; a list with for_each) and hdr.
type Webhook struct {
	// Fields are the register config keys (n8n's trigger param names).
	Fields []workflow.NodeSchemaField `json:"fields,omitempty"`
	Create []Step                     `json:"create"`
	// ID (a template) is the hook id kept for check and delete; Extra
	// (templates) are more values kept, read back as hook.extra.<key>.
	ID    string            `json:"id,omitempty"`
	Extra map[string]string `json:"extra,omitempty"`
	// Delete removes the hook; a 404 or 410 counts as removed.
	Delete []Step `json:"delete"`
	// Check, when set, tells whether the stored hook still exists.
	Check *WebhookCheck `json:"check,omitempty"`
	// Verify, when set, checks each delivery's signature.
	Verify *WebhookVerify `json:"verify,omitempty"`
}

// WebhookCheck is a request answering whether the stored hook exists: 2xx
// yes (or ExistsWhen, a template over res, when set), 404/410 no.
type WebhookCheck struct {
	Step
	ExistsWhen string `json:"exists_when,omitempty"`
}

// WebhookVerify checks a delivery's signature: Signature (a template over
// hdr, the delivery's headers) must equal encode(HMAC(secret, message)).
// Message defaults to the raw body ({{ req.body }} in a template); Secret
// to {{ hook.secret }}. Alg "token" compares the signature with the secret
// itself (GitLab's X-Gitlab-Token, Telegram's secret token).
type WebhookVerify struct {
	Signature string `json:"signature"`
	Message   string `json:"message,omitempty"`
	Secret    string `json:"secret,omitempty"`
	Alg       string `json:"alg,omitempty"`      // sha256 (default), sha1, sha512, token
	Encoding  string `json:"encoding,omitempty"` // hex (default) or base64
	Prefix    string `json:"prefix,omitempty"`   // stripped from the signature ("sha256=")
	// SecretEncoding "hex" or "base64" decodes the secret into the HMAC
	// key (Onfleet's hex signing secret); default: its text.
	SecretEncoding string `json:"secret_encoding,omitempty"`
}

// webhookRegistrar is the workflow.WebhookRegistrar of one package. It
// looks the package up on every call, so a reinstalled package's block is
// used without a restart.
type webhookRegistrar struct{ id string }

// registerWebhooks registers a registrar named after each package with a
// webhook block, the trigger.webhook "register_with" value. A name the
// engine already has (its built-in "typeform" and "calendly") stays the
// engine's.
func registerWebhooks() {
	for _, p := range packages() {
		if name := p.webhookName(); name != "" {
			workflow.RegisterWebhookRegistrar(name, webhookRegistrar{id: p.ID})
		}
	}
}

// webhookName is the registrar name of p's webhook block: its id, or ""
// when p has no block or the engine owns that name.
func (p *Package) webhookName() string {
	if p.Webhook == nil {
		return ""
	}
	if r := workflow.WebhookRegistrarFor(p.ID); r != nil {
		if _, ours := r.(webhookRegistrar); !ours {
			return ""
		}
	}
	return p.ID
}

func (w webhookRegistrar) pkg() (*Package, error) {
	p, ok := ByID(w.id)
	if !ok || p.Webhook == nil {
		return nil, fmt.Errorf("apipkg %s: no installed package registers webhooks under this name", w.id)
	}
	return p, nil
}

// hookMap is the hook template root.
func hookMap(hook workflow.AppWebhook, stored workflow.AppWebhookHook) map[string]any {
	extra := make(map[string]any, len(stored.Extra))
	for k, v := range stored.Extra {
		extra[k] = v
	}
	return map[string]any{
		"url": hook.URL, "path": hook.Path, "secret": hook.Secret, "id": stored.ID,
		"workflow_id": hook.WorkflowID, "node_id": hook.NodeID, "extra": extra,
	}
}

// hookParams is the register config: the node config without the resolved
// credential, with the webhook fields' defaults filled in.
func (wh *Webhook) hookParams(cfg map[string]any) map[string]any {
	params := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if k != "credential" {
			params[k] = v
		}
	}
	for _, f := range wh.Fields {
		if _, ok := params[f.Key]; !ok && f.Default != nil {
			params[f.Key] = f.Default
		}
	}
	return params
}

// hookRun prepares an opRun whose steps are the webhook's requests.
func (p *Package) hookRun(ctx context.Context, what string, hook workflow.AppWebhook, stored workflow.AppWebhookHook) (*opRun, error) {
	cred, _ := hook.Config["credential"].(map[string]any)
	if p.needsCredential() && len(cred) == 0 {
		return nil, fmt.Errorf("apipkg %s webhook: no credential: set the trigger's credential_id to a %s connection (monoagentcli connect %s)",
			p.ID, p.PrimaryPlatform(), p.PrimaryPlatform())
	}
	params := p.Webhook.hookParams(hook.Config)
	if what == "create" {
		for _, f := range p.Webhook.Fields {
			if f.Required && isEmpty(params[f.Key]) {
				return nil, fmt.Errorf("apipkg %s webhook: register_config field %q (%s) is required", p.ID, f.Key, f.Label)
			}
		}
	}
	credDef, cred, err := p.prepareCredential(ctx, cred)
	if err != nil {
		return nil, err
	}
	return &opRun{pkg: p, op: &Operation{Operation: "webhook " + what}, params: params, credDef: credDef,
		scope: Scope{P: params, Cred: cred, Item: map[string]any{}, Hook: hookMap(hook, stored)}}, nil
}

// isGone reports whether err is a 404 or 410 answer.
func isGone(err error) bool {
	var se *statusError
	return errors.As(err, &se) && (se.code == http.StatusNotFound || se.code == http.StatusGone)
}

// runSteps sends steps in order (pre/pres see earlier ones) and returns
// the last response and its headers. With tolerateGone a 404/410 answer
// counts as done and the remaining steps still run.
func (r *opRun) runSteps(ctx context.Context, steps []Step, tolerateGone bool) (any, headerMap, error) {
	var last any
	hdr := headerMap{}
	var errs []error
	send := func(i int, st Step, sc Scope) any {
		resp, err := r.sendStep(ctx, st, sc)
		if err != nil {
			if !(tolerateGone && isGone(err)) {
				errs = append(errs, fmt.Errorf("step %d: %w", i, err))
			}
			return nil
		}
		if resp == nil {
			return nil
		}
		hdr = headerMap{}
		for k, vs := range resp.header {
			if len(vs) > 0 {
				hdr[k] = vs[0]
			}
		}
		if len(strings.TrimSpace(string(resp.body))) == 0 {
			return nil
		}
		return decodeBody(resp.body)
	}
	for i, st := range steps {
		if st.ForEach != "" {
			lv, err := renderString(st.ForEach, r.scope)
			if err != nil {
				return nil, nil, r.errf("step %d for_each: %w", i, err)
			}
			list, _ := lv.([]any)
			collected := make([]any, 0, len(list))
			for j, el := range list {
				sc := r.scope
				sc.El, sc.Idx = el, j
				collected = append(collected, send(i, st, sc))
			}
			last = collected
		} else {
			last = send(i, st, r.scope)
		}
		if len(errs) > 0 && !tolerateGone {
			return nil, nil, errs[0]
		}
		r.scope.Pres = append(r.scope.Pres, last)
		r.scope.Pre = last
		r.scope.Hdr = hdr // a login step's token header (Form.io x-jwt-token)
	}
	return last, hdr, errors.Join(errs...)
}

// Register implements workflow.WebhookRegistrar.
func (w webhookRegistrar) Register(ctx context.Context, hook workflow.AppWebhook) (workflow.AppWebhookHook, error) {
	p, err := w.pkg()
	if err != nil {
		return workflow.AppWebhookHook{}, err
	}
	r, err := p.hookRun(ctx, "create", hook, workflow.AppWebhookHook{})
	if err != nil {
		return workflow.AppWebhookHook{}, err
	}
	res, hdr, err := r.runSteps(ctx, p.Webhook.Create, false)
	if err != nil {
		return workflow.AppWebhookHook{}, err
	}
	sc := r.scope
	sc.Res, sc.Hdr = res, hdr
	got := workflow.AppWebhookHook{Extra: map[string]any{}}
	if t := p.Webhook.ID; t != "" {
		v, err := renderString(t, sc)
		if err != nil {
			return got, r.errf("id: %w", err)
		}
		if got.ID = stringify(v); got.ID == "" {
			return got, r.errf("the create answer has no hook id (%s)", t)
		}
	}
	for k, t := range p.Webhook.Extra {
		v, err := renderString(t, sc)
		if err != nil {
			return got, r.errf("extra.%s: %w", k, err)
		}
		if !isEmpty(v) {
			got.Extra[k] = v
		}
	}
	return got, nil
}

// Unregister implements workflow.WebhookRegistrar.
func (w webhookRegistrar) Unregister(ctx context.Context, hook workflow.AppWebhook, stored workflow.AppWebhookHook) error {
	p, err := w.pkg()
	if err != nil {
		return err
	}
	r, err := p.hookRun(ctx, "delete", hook, stored)
	if err != nil {
		return err
	}
	_, _, err = r.runSteps(ctx, p.Webhook.Delete, true)
	return err
}

// Exists implements workflow.WebhookChecker; without a check block the
// stored hook is assumed to exist.
func (w webhookRegistrar) Exists(ctx context.Context, hook workflow.AppWebhook, stored workflow.AppWebhookHook) (bool, error) {
	p, err := w.pkg()
	if err != nil {
		return false, err
	}
	c := p.Webhook.Check
	if c == nil {
		return true, nil
	}
	r, err := p.hookRun(ctx, "check", hook, stored)
	if err != nil {
		return false, err
	}
	res, _, err := r.runSteps(ctx, []Step{c.Step}, false)
	if isGone(err) {
		return false, nil
	}
	if err != nil || c.ExistsWhen == "" {
		return err == nil, err
	}
	sc := r.scope
	sc.Res = res
	v, err := renderString(c.ExistsWhen, sc)
	if err != nil {
		return false, r.errf("check.exists_when: %w", err)
	}
	return truthy(v), nil
}

// VerifyDelivery implements workflow.WebhookVerifier.
func (w webhookRegistrar) VerifyDelivery(header http.Header, body []byte, hook workflow.AppWebhook, stored workflow.AppWebhookHook) error {
	p, err := w.pkg()
	if err != nil || p.Webhook.Verify == nil {
		return err
	}
	cred, _ := hook.Config["credential"].(map[string]any)
	hdr := headerMap{}
	for k, vs := range header {
		if len(vs) > 0 {
			hdr[k] = vs[0]
		}
	}
	sc := Scope{P: p.Webhook.hookParams(hook.Config), Cred: cred, Hook: hookMap(hook, stored),
		Hdr: hdr, Req: map[string]any{"body": string(body)}}
	return p.Webhook.Verify.check(sc, body)
}
