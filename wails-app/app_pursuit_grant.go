// wails-app: the single-use grant a pursuit start needs besides the typed phrase.
//
// Any script in the web view can call a binding, and the phrase can be derived from what the preview shows. So a start also
// needs what such a script cannot make: the token of a preview of this session, kept only here, bound to the exact preview
// bytes (so to the goal hash, the chat, the limits and the consent digest), single use, ten minutes, replaced by a newer
// preview of the same session. The token goes to the page, so it is not a secret from a page script: it stops replay, a
// stale preview and a swap of limits. The click in the native dialog is what the script cannot give.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strconv"
	"sync"
)

type pursuitGrant struct {
	token [16]byte
	sum   [32]byte
	exp   int64 // unix nanoseconds
	facts pursuitFacts
}

type pursuitGrantStore struct {
	mu sync.Mutex
	m  map[string]*pursuitGrant // by profile and session: one preview at a time for each
}

var pursuitGrants pursuitGrantStore

func (g *pursuitGrantStore) reset() {
	g.mu.Lock()
	g.m = nil
	g.mu.Unlock()
}

func pursuitGrantKey(profile string, session int64) string {
	return profile + "\x00" + strconv.FormatInt(session, 10)
}

// issue replaces the grant of (profile, session) with one for these facts and the limits they resolved, and returns its token.
func (g *pursuitGrantStore) issue(profile string, f pursuitFacts) (string, error) {
	var gr pursuitGrant
	if _, err := rand.Read(gr.token[:]); err != nil {
		return "", err
	}
	gr.sum = sha256.Sum256([]byte(f.bound(PursuitLimits{ParkedMax: f.limits.ParkedMax, Pct: f.limits.Pct, WakesMax: f.limits.WakesMax})))
	now := doomsdayNow()
	gr.exp, gr.facts = now.Add(doomsdayGrantTTL).UnixNano(), f
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m == nil {
		g.m = map[string]*pursuitGrant{}
	}
	for k, o := range g.m { // a grant that ran out is gone
		if now.UnixNano() >= o.exp {
			delete(g.m, k)
		}
	}
	g.m[pursuitGrantKey(profile, f.session)] = &gr
	return hex.EncodeToString(gr.token[:]), nil
}

var errPursuitGrant = errors.New("the preview is stale, used, replaced by a newer one, or for other limits: read the pursuit again")

// peek checks the grant of (profile, session) without spending it: the token, the limits the start names against the ones
// the preview resolved, and the time. It returns the facts the dialog and the phrase are built from.
func (g *pursuitGrantStore) peek(profile string, session int64, token string, lim PursuitLimits) (pursuitFacts, error) {
	raw, err := hex.DecodeString(token)
	g.mu.Lock()
	defer g.mu.Unlock()
	gr := g.m[pursuitGrantKey(profile, session)]
	if err != nil || gr == nil || len(raw) != len(gr.token) || subtle.ConstantTimeCompare(raw, gr.token[:]) != 1 {
		return pursuitFacts{}, errPursuitGrant
	}
	sum := sha256.Sum256([]byte(gr.facts.bound(lim)))
	if subtle.ConstantTimeCompare(sum[:], gr.sum[:]) != 1 || doomsdayNow().UnixNano() >= gr.exp {
		return pursuitFacts{}, errPursuitGrant
	}
	return gr.facts, nil
}

// consume spends the grant if it is still this token: called when the native dialog confirmed, so a decline leaves the
// preview usable and a confirmed start can never be repeated with it.
func (g *pursuitGrantStore) consume(profile string, session int64, token string) error {
	raw, err := hex.DecodeString(token)
	g.mu.Lock()
	defer g.mu.Unlock()
	k := pursuitGrantKey(profile, session)
	gr := g.m[k]
	if err != nil || gr == nil || subtle.ConstantTimeCompare(raw, gr.token[:]) != 1 || doomsdayNow().UnixNano() >= gr.exp {
		return errPursuitGrant
	}
	delete(g.m, k)
	return nil
}
