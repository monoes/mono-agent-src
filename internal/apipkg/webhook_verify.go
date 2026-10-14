package apipkg

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // vendors sign with HMAC-SHA1 (Trello, Twilio)
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"strings"
)

// webhookAlgs are the verify.alg values; "token" compares without a MAC.
var webhookAlgs = map[string]func() hash.Hash{"sha256": sha256.New, "sha1": sha1.New, "sha512": sha512.New, "token": nil}

// check verifies one delivery (see WebhookVerify). A secret that renders
// empty (the vendor returned none) skips the check.
func (v *WebhookVerify) check(sc Scope, body []byte) error {
	secretT := v.Secret
	if secretT == "" {
		secretT = "{{ hook.secret }}"
	}
	secret, err := renderText(secretT, sc)
	if err != nil {
		return fmt.Errorf("webhook verify secret: %w", err)
	}
	if secret == "" {
		return nil
	}
	sig, err := renderText(v.Signature, sc)
	if err != nil {
		return fmt.Errorf("webhook verify signature: %w", err)
	}
	sig = strings.TrimSpace(sig)
	if sig == "" {
		return errors.New("the delivery carries no signature")
	}
	if v.Prefix != "" {
		var ok bool
		if sig, ok = strings.CutPrefix(sig, v.Prefix); !ok {
			return fmt.Errorf("the delivery's signature does not start with %q", v.Prefix)
		}
	}
	alg := v.Alg
	if alg == "" {
		alg = "sha256"
	}
	if alg == "token" {
		if subtle.ConstantTimeCompare([]byte(sig), []byte(secret)) != 1 {
			return errors.New("the delivery's token does not match")
		}
		return nil
	}
	msg := body
	if v.Message != "" {
		s, err := renderText(v.Message, sc)
		if err != nil {
			return fmt.Errorf("webhook verify message: %w", err)
		}
		msg = []byte(s)
	}
	key := []byte(secret)
	switch v.SecretEncoding {
	case "hex":
		key, err = hex.DecodeString(secret)
	case "base64":
		key, err = base64.StdEncoding.DecodeString(secret)
	}
	if err != nil {
		return fmt.Errorf("webhook verify secret: not %s: %w", v.SecretEncoding, err)
	}
	mac := hmac.New(webhookAlgs[alg], key)
	mac.Write(msg)
	var got []byte
	if v.Encoding == "base64" {
		got, err = base64.StdEncoding.DecodeString(sig)
	} else {
		got, err = hex.DecodeString(strings.ToLower(sig))
	}
	if err != nil || !hmac.Equal(mac.Sum(nil), got) {
		return errors.New("the delivery's signature does not match")
	}
	return nil
}
