package hilsend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/secrets"
)

// A held send keeps a reference to a granted secret it carries, never the
// value (#1594): the guard replaces what the run read by a token bound to the
// card, and the approval turns the tokens back into values if the grant still
// holds (secrets.ResolveHeld). Nothing else resolves: a literal @secret: in a
// card is refused at approval.

// heldVerifier recognises the tags the guard minted for this card: the card
// keeps only their hashes (replay.RefHashes), never anything a reader of the
// card could use to write a reference of its own.
func heldVerifier(hashes map[string]string) func(name, mac string) bool {
	return func(name, mac string) bool { return secrets.HeldMACMatches(hashes[name], mac) }
}

// mapStrings returns v with f applied to every string in it, map keys too.
func mapStrings(v interface{}, f func(string) (string, error)) (interface{}, error) {
	switch t := v.(type) {
	case string:
		return f(t)
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, e := range t {
			r, err := mapStrings(e, f)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, e := range t {
			nk, err := f(k)
			if err != nil {
				return nil, err
			}
			r, err := mapStrings(e, f)
			if err != nil {
				return nil, err
			}
			out[nk] = r
		}
		return out, nil
	}
	return v, nil
}

// viaJSON applies f to every string of *ptr, which round-trips through JSON.
func viaJSON(ptr interface{}, f func(string) (string, error)) error {
	raw, err := json.Marshal(ptr)
	if err != nil {
		return err
	}
	var generic interface{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return err
	}
	out, err := mapStrings(generic, f)
	if err != nil {
		return err
	}
	raw, err = json.Marshal(out)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, ptr)
}

// heldify replaces every secret value the run read by a token bound to card
// id, in the payload and the replay. A value that cannot be a token (an
// encoded form of it, a name outside the token charset) is refused: such a
// card is not queued.
func heldify(ctx context.Context, p *Payload, rp *replay) (map[string]string, error) {
	macs := map[string]string{}
	f := func(s string) (string, error) {
		out, ok := secrets.UnresolveTracked(ctx, s, func(name string) string {
			if macs[name] == "" {
				macs[name] = secrets.HeldNewMAC()
			}
			return secrets.HeldToken(name, macs[name])
		})
		if !ok {
			return s, fmt.Errorf("it carries a secret the run read in a form that cannot be held as a reference (encoded, or a name outside letters, digits and . _ -)")
		}
		return out, nil
	}
	if err := viaJSON(p, f); err != nil {
		return nil, err
	}
	if err := viaJSON(rp, f); err != nil {
		return nil, err
	}
	hashes := map[string]string{}
	for n, m := range macs {
		hashes[n] = secrets.HeldMACHash(m)
	}
	return hashes, nil
}

var friendlyRE = regexp.MustCompile(`\[\[held-secret:([A-Za-z0-9._-]+):[0-9a-f]{32}\]\]`)

// Friendly shows a held reference as "<NAME secret, held>" for a person
// reading the card; the stored text and the JSON keep the token.
func Friendly(s string) string { return friendlyRE.ReplaceAllString(s, "<$1 secret, held>") }

// createHeld queues the card with references in place of granted secrets.
func (s Store) createHeld(ctx context.Context, p Payload, rp replay) (Payload, error) {
	p.ID = uuid.NewString()
	if len(secrets.ResolvedValues(ctx)) > 0 {
		hashes, err := heldify(ctx, &p, &rp)
		if err != nil {
			return p, fmt.Errorf("this send is not queued for approval: %w", err)
		}
		if len(hashes) > 0 {
			rp.RefHashes = hashes
		}
	}
	return s.create(ctx, p, rp)
}

// resolveHeldReplay is the config and input an approval sends, with the
// references of the card turned into values (or only checked, dry). The text
// and subject edits are in cfg already.
func (s Store) resolveHeldReplay(ctx context.Context, p Payload, rp replay, cfg map[string]interface{}, dry bool) (map[string]interface{}, []map[string]interface{}, []string, error) {
	var read []string
	o := secrets.HeldOptions{Verify: heldVerifier(rp.RefHashes), NodeType: rp.NodeType, Host: p.Host, Card: p.ID, Dry: dry, Resolved: &read}
	f := func(str string) (string, error) { return secrets.ResolveHeld(ctx, s.DB, s.Profile, str, o) }
	var cfgOut map[string]interface{}
	holder := struct {
		C map[string]interface{}   `json:"c"`
		I []map[string]interface{} `json:"i"`
	}{cfg, rp.Input}
	if err := viaJSON(&holder, f); err != nil {
		return nil, nil, nil, fmt.Errorf("send item %s: %w (reject it and have the agent prepare it again, or edit the reference out of its text)", p.ID, err)
	}
	cfgOut = holder.C
	return cfgOut, holder.I, read, nil
}
