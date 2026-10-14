package apipkg

import (
	"crypto/md5" //nolint:gosec // content fingerprint the API asks for, not a security use
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // content fingerprint the API asks for, not a security use
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/nacl/box"
)

// hashHex is the md5/sha1/sha256 filter: the hex digest of the value's
// text, or of raw bytes. With the argument 'base64' the value is base64
// decoded first, so a file's digest is {{ bin.base64 | sha256:'base64' }}.
func hashHex(name string, v, arg any) (any, error) {
	var data []byte
	switch x := v.(type) {
	case []byte:
		data = x
	default:
		data = []byte(stringify(v))
	}
	switch enc := stringify(arg); enc {
	case "":
	case "base64":
		b, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(data)), ""))
		if err != nil {
			return nil, fmt.Errorf("%s: value is not base64: %w", name, err)
		}
		data = b
	default:
		return nil, fmt.Errorf("%s: unknown argument %q (only 'base64')", name, enc)
	}
	var h hash.Hash
	switch name {
	case "md5":
		h = md5.New() //nolint:gosec // see import
	case "sha1":
		h = sha1.New() //nolint:gosec // see import
	default:
		h = sha256.New()
	}
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// modulo is the mod filter: the remainder of a division, with the sign of
// the divisor (so {{ -1 | mod:7 }} is 6, handy for day arithmetic).
func modulo(v, arg any) (any, error) {
	a, err := toFloat(v)
	if err != nil || a == nil {
		return nil, err
	}
	b, err := toFloat(arg)
	if err != nil || b == nil {
		return nil, fmt.Errorf("mod: argument %v is not a number", arg)
	}
	x, y := a.(float64), b.(float64)
	if y == 0 {
		return nil, fmt.Errorf("mod: division by zero")
	}
	r := math.Mod(x, y)
	if r != 0 && (r < 0) != (y < 0) {
		r += y
	}
	if r == math.Trunc(r) {
		return int64(r), nil
	}
	return r, nil
}

// isoWeekday is the weekday filter: a date (the date filter's formats or
// an epoch) → its ISO weekday in UTC, 1 = Monday … 7 = Sunday.
func isoWeekday(v any) (any, error) {
	s, err := formatDate(v, "YYYY-MM-DD")
	if err != nil || s == nil {
		return nil, err
	}
	t, err := time.Parse("2006-01-02", s.(string))
	if err != nil {
		return nil, fmt.Errorf("weekday: %w", err)
	}
	wd := int64(t.Weekday())
	if wd == 0 {
		wd = 7
	}
	return wd, nil
}

// dotnetDateRE matches a .NET JSON date: /Date(1700000000000)/ with an
// optional UTC offset (/Date(1700000000000+0100)/).
var dotnetDateRE = regexp.MustCompile(`^/Date\((-?\d+)([+-]\d{4})?\)/$`)

// dotnetDates is the dotnet_date filter: a .NET JSON date string → ISO
// 8601 in UTC with milliseconds; objects and lists are converted deeply
// (every matching string value), other values pass unchanged. The ms
// value is already UTC, so the offset only tells the server's zone.
func dotnetDates(v any) any {
	switch x := v.(type) {
	case string:
		m := dotnetDateRE.FindStringSubmatch(x)
		if m == nil {
			return x
		}
		ms, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return x
		}
		return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, el := range x {
			out[k] = dotnetDates(el)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, el := range x {
			out[i] = dotnetDates(el)
		}
		return out
	}
	return v
}

// sealBox is the sealbox filter: the value's text encrypted for the
// base64 X25519 public key arg as a libsodium sealed box
// (crypto_box_seal), base64-encoded. Venafi wants keystore passphrases
// sealed with the certificate's edge encryption key.
func sealBox(v, arg any) (any, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(stringify(arg)))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("sealbox: the key must be a base64 32-byte public key")
	}
	var pk [32]byte
	copy(pk[:], key)
	sealed, err := box.SealAnonymous(nil, []byte(stringify(v)), &pk, rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("sealbox: %w", err)
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// mapThrough replaces a value found in table ("from=to,from2=to2") by its
// replacement; other values pass unchanged. A list is mapped element by
// element (n8n trigger event names → the vendor's: Wise
// "balanceCredit=balances#credit", Invoice Ninja "create_client=1").
func mapThrough(v any, table string) any {
	if l, ok := v.([]any); ok {
		out := make([]any, len(l))
		for i, el := range l {
			out[i] = mapThrough(el, table)
		}
		return out
	}
	s := stringify(v)
	for _, pair := range strings.Split(table, ",") {
		from, to, ok := strings.Cut(pair, "=")
		if ok && strings.TrimSpace(from) == s {
			return strings.TrimSpace(to)
		}
	}
	return v
}
