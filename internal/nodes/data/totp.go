package data

import (
	"context"
	"crypto/hmac"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// TOTPNode generates a time-based one-time password (RFC 6238) from a base32
// secret. Type: "data.totp" (n8n: TOTP).
//
// Config:
//
//	operation   generate_secret (n8n's name; it generates the current code)
//	secret      base32 secret (spaces and lowercase allowed)
//	label       optional "issuer:username"; must contain a colon when set
//	algorithm   SHA1 (default) | SHA224 | SHA256 | SHA384 | SHA512 | SHA3-224 | SHA3-256 | SHA3-384 | SHA3-512
//	digits      code length (default 6)
//	period      seconds per code (default 30)
//
// Output per item: {token, secondsRemaining} — n8n's field names, so
// imported expressions keep working.
type TOTPNode struct {
	now func() time.Time
}

func (n *TOTPNode) Type() string { return "data.totp" }

// PublishesToThirdParty: data.totp sends nothing off this machine (issue #1418).
func (TOTPNode) PublishesToThirdParty() bool { return false }

func (n *TOTPNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	op := cfgString(config, "operation", "generate_secret")
	if op != "generate_secret" && op != "generateSecret" {
		return nil, fmt.Errorf("data.totp: unknown operation %q (want generate_secret)", op)
	}
	if label := cfgString(config, "label", ""); label != "" && !strings.Contains(label, ":") {
		return nil, fmt.Errorf("data.totp: label: malformed, expected issuer:username")
	}
	secret, err := decodeTOTPSecret(cfgString(config, "secret", ""))
	if err != nil {
		return nil, fmt.Errorf("data.totp: secret: %w", err)
	}
	ht, err := lookupHash(cfgString(config, "algorithm", "SHA1"))
	if err != nil {
		return nil, fmt.Errorf("data.totp: algorithm: %w", err)
	}
	digits, err := cfgInt(config, "digits", 6)
	if err == nil && digits == 0 {
		digits = 6
	}
	if err != nil || digits < 1 || digits > 10 {
		return nil, fmt.Errorf("data.totp: digits: must be between 1 and 10")
	}
	period, err := cfgInt(config, "period", 30)
	if err == nil && period == 0 {
		period = 30
	}
	if err != nil || period < 1 {
		return nil, fmt.Errorf("data.totp: period: must be a positive number of seconds")
	}
	now := time.Now()
	if n.now != nil {
		now = n.now()
	}
	token := totpCode(secret, ht, digits, uint64(now.Unix())/uint64(period))
	remaining := period - int(now.Unix()%int64(period))

	items := input.Items
	if len(items) == 0 {
		items = []workflow.Item{{}}
	}
	out := make([]workflow.Item, 0, len(items))
	for range items {
		out = append(out, workflow.Item{JSON: map[string]interface{}{"token": token, "secondsRemaining": remaining}})
	}
	return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
}

func decodeTOTPSecret(s string) ([]byte, error) {
	s = strings.ToUpper(strings.Join(strings.Fields(s), ""))
	s = strings.ReplaceAll(s, "-", "")
	if s == "" {
		return nil, fmt.Errorf("is required")
	}
	b, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return nil, fmt.Errorf("not a valid base32 secret: %w", err)
	}
	return b, nil
}

// totpCode is HOTP (RFC 4226) over the time-step counter.
func totpCode(secret []byte, ht hashType, digits int, counter uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(ht.newHash, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(math.Pow10(digits))
	if digits >= 10 {
		return fmt.Sprintf("%010d", code)
	}
	return fmt.Sprintf("%0*d", digits, code%mod)
}
