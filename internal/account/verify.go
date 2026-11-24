package account

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Receipt is what a verified access token proves.
type Receipt struct {
	Sub       string
	Plan      string // "free" when the token has no plan claim
	IssuedAt  time.Time
	ExpiresAt time.Time
	KID       string
}

// VerifyError is the typed reason a token was refused: ReasonInvalid
// (structure, signature or claims), ReasonKeyUnknown (the kid is not pinned) or
// ReasonClockSkew (iat is more than ClockSkew ahead of now). Its message names
// the failing check. The only text it copies from the token is the kid of a
// ReasonKeyUnknown error, quoted and cut to 64 bytes; a ReasonClockSkew message
// also says how far ahead iat is, as a duration. It never carries the payload,
// the signature or the token itself.
type VerifyError struct {
	Reason Reason
	why    string
}

func (e *VerifyError) Error() string {
	switch e.Reason {
	case ReasonKeyUnknown:
		return fmt.Sprintf("account: token key %q is not pinned in this build", e.why)
	case ReasonClockSkew:
		return "account: token was issued in the future (" + e.why + "); check the system clock"
	}
	if e.why == "" {
		return "account: token is invalid"
	}
	return "account: token is invalid: " + e.why
}

func invalid(why string) *VerifyError { return &VerifyError{Reason: ReasonInvalid, why: why} }

// maxTokenBytes bounds the work a hostile session.json can cause.
const maxTokenBytes = 8 << 10

// maxKIDInMessage bounds how much of a kid an error message quotes: whoever
// made the token chose the kid, and it can be most of the token's 8 KiB.
const maxKIDInMessage = 64

// clipKID cuts kid to at most maxKIDInMessage bytes, at a character boundary,
// and marks the cut with "...".
func clipKID(kid string) string {
	if len(kid) <= maxKIDInMessage {
		return kid
	}
	cut := maxKIDInMessage
	for cut > 0 && !utf8.RuneStart(kid[cut]) {
		cut--
	}
	return kid[:cut] + "..."
}

// Verify checks a compact JWS access token and returns its receipt. It accepts
// one algorithm (EdDSA), only keys pinned in this build, and never reads the
// jku, jwk, x5u or x5c headers. It does not reject an expired token: the grace
// period needs the receipt of one. now is only used for the clock-skew check.
func Verify(token string, now time.Time) (*Receipt, error) {
	r, verr := verifyToken(token)
	if verr != nil {
		return nil, verr
	}
	if r.IssuedAt.After(now.Add(ClockSkew)) {
		return nil, &VerifyError{Reason: ReasonClockSkew, why: "issued " + r.IssuedAt.Sub(now).Round(time.Second).String() + " ahead"}
	}
	return r, nil
}

// verifyToken is Verify without the clock: structure, algorithm, key,
// signature, claims and lifetime, checked in that order, so a claim is never
// read from a token whose signature did not verify.
func verifyToken(token string) (*Receipt, *VerifyError) {
	if len(token) > maxTokenBytes {
		return nil, invalid("too large")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || slices.Contains(parts, "") {
		return nil, invalid("not a compact JWS")
	}
	headerJSON, ok := decodeSegment(parts[0])
	if !ok {
		return nil, invalid("header is not base64url")
	}
	payloadJSON, ok := decodeSegment(parts[1])
	if !ok {
		return nil, invalid("payload is not base64url")
	}
	sig, ok := decodeSegment(parts[2])
	if !ok || len(sig) != ed25519.SignatureSize {
		return nil, invalid("signature is not a base64url Ed25519 signature")
	}

	var header map[string]json.RawMessage
	if json.Unmarshal(headerJSON, &header) != nil {
		return nil, invalid("header is not a JSON object")
	}
	var alg, kid string
	if _, ok := field(header, "alg", &alg); !ok || alg != "EdDSA" {
		return nil, invalid("alg is not EdDSA")
	}
	if _, has := header["crit"]; has {
		return nil, invalid("critical headers are not supported")
	}
	if _, ok := field(header, "kid", &kid); !ok || kid == "" {
		return nil, invalid("no kid")
	}
	key, ok := lookupKey(kid)
	if !ok {
		return nil, &VerifyError{Reason: ReasonKeyUnknown, why: clipKID(kid)}
	}
	if len(key.Public) != ed25519.PublicKeySize {
		return nil, invalid("pinned key is malformed")
	}
	if !ed25519.Verify(key.Public, []byte(parts[0]+"."+parts[1]), sig) {
		return nil, invalid("signature does not verify")
	}

	var claims map[string]json.RawMessage
	if json.Unmarshal(payloadJSON, &claims) != nil {
		return nil, invalid("payload is not a JSON object")
	}
	r, verr := receiptFromClaims(claims)
	if verr != nil {
		return nil, verr
	}
	r.KID = kid
	return r, nil
}

// decodeSegment decodes one unpadded base64url segment. It is stricter than
// the standard decoder, which skips \r and \n: any byte outside the alphabet,
// and any non-canonical trailing bits, is an error.
func decodeSegment(s string) ([]byte, bool) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return nil, false
		}
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return b, err == nil
}

// receiptFromClaims checks the claims of a token whose signature verified.
//
// Accepted limit (r05-D6): the access token is a bearer with no machine binding, so a copied session.json passes the
// gate for the token's life (24h at most, plus the grace to iat+24h). The refresh token is folder/inode bound and does
// not travel. The gate is soft by design (the user owns the binary), so no device claim is added.
func receiptFromClaims(claims map[string]json.RawMessage) (*Receipt, *VerifyError) {
	var iss, sub string
	if _, ok := field(claims, "iss", &iss); !ok || iss != Issuer {
		return nil, invalid("issuer")
	}
	if !audienceMatches(claims["aud"]) {
		return nil, invalid("audience")
	}
	if !clientMatches(claims) {
		return nil, invalid("client")
	}
	if _, ok := field(claims, "sub", &sub); !ok || sub == "" {
		return nil, invalid("sub")
	}
	iat, okIat := numericDate(claims["iat"])
	exp, okExp := numericDate(claims["exp"])
	if !okIat || !okExp {
		return nil, invalid("iat or exp")
	}
	if !exp.After(iat) || exp.Sub(iat) > MaxTokenLife {
		return nil, invalid("lifetime")
	}
	// plan is read and not enforced (spec D14): a missing or odd one is "free".
	plan := "free"
	var p string
	if raw, has := claims["plan"]; has && json.Unmarshal(raw, &p) == nil && p != "" {
		plan = p
	}
	return &Receipt{Sub: sub, Plan: plan, IssuedAt: iat, ExpiresAt: exp}, nil
}

// field decodes the claim or header named key into dst. present reports
// whether it exists; ok is false when it exists with the wrong type. Keys match
// exactly: encoding/json's case-insensitive struct matching is not used.
func field(m map[string]json.RawMessage, key string, dst any) (present, ok bool) {
	raw, present := m[key]
	if !present {
		return false, true
	}
	return true, json.Unmarshal(raw, dst) == nil
}

// audienceMatches accepts aud as a string or an array of strings that
// contains Audience.
func audienceMatches(raw json.RawMessage) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == Audience
	}
	var many []string
	return json.Unmarshal(raw, &many) == nil && slices.Contains(many, Audience)
}

// clientMatches requires the client claim (azp, or client_id as RFC 9068 names
// it) and every one that is present to be ClientID.
func clientMatches(claims map[string]json.RawMessage) bool {
	seen := 0
	for _, name := range []string{"azp", "client_id"} {
		var v string
		present, ok := field(claims, name, &v)
		if !present {
			continue
		}
		if !ok || v != ClientID {
			return false
		}
		seen++
	}
	return seen > 0
}

// numericDate reads a JWT NumericDate (seconds, possibly fractional).
func numericDate(raw json.RawMessage) (time.Time, bool) {
	var v *float64
	if json.Unmarshal(raw, &v) != nil || v == nil || *v < 0 || *v > 1e11 {
		return time.Time{}, false
	}
	sec := int64(*v)
	return time.Unix(sec, int64((*v-float64(sec))*1e9)).UTC(), true
}
