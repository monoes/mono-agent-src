package guardrails

import (
	"math/big"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// piiEntity is one personal-data type: its n8n entity name (also the
// <NAME> placeholder sanitize writes), the pattern that finds candidates
// and an optional check (checksum, value ranges) a candidate must pass.
type piiEntity struct {
	name  string
	re    *regexp.Regexp
	valid func(string) bool
}

// piiEntities are detected in this order. A span claimed by an earlier
// entity is not reported again by a later one, so specific, checksummed
// types come before broad ones (an IBAN is not also a bank number).
var piiEntities = []piiEntity{
	{"EMAIL_ADDRESS", regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}\b`), nil},
	{"CREDIT_CARD", regexp.MustCompile(`\b(?:\d{4}[ -]?){3}\d{1,4}(?:[ -]?\d{3})?\b|\b\d{4}[ -]?\d{6}[ -]?\d{4,5}\b`), validCard},
	{"IBAN_CODE", regexp.MustCompile(`\b[A-Z]{2}\d{2}(?: ?[A-Z0-9]{4}){2,7}(?: ?[A-Z0-9]{1,3})?\b`), validIBAN},
	{"CRYPTO", regexp.MustCompile(`\b(?:[13][a-km-zA-HJ-NP-Z1-9]{25,34}|bc1[ac-hj-np-z02-9]{11,71}|0x[a-fA-F0-9]{40})\b`), nil},
	{"IP_ADDRESS", regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b|(?:[0-9A-Fa-f]{0,4}:){2,7}[0-9A-Fa-f]{0,4}`), validIP},
	{"FI_PERSONAL_IDENTITY_CODE", regexp.MustCompile(`\b\d{6}[-+A-FU-Y]\d{3}[0-9A-Y]\b`), validHETU},
	{"ES_NIE", regexp.MustCompile(`\b[XYZ]-?\d{7}-?[A-Z]\b`), validNIE},
	{"ES_NIF", regexp.MustCompile(`\b\d{8}-?[A-Z]\b`), validNIF},
	{"IT_FISCAL_CODE", regexp.MustCompile(`\b[A-Z]{6}\d{2}[A-EHLMPR-T]\d{2}[A-Z]\d{3}[A-Z]\b`), nil},
	{"IT_VAT_CODE", regexp.MustCompile(`\bIT ?\d{11}\b`), nil},
	{"UK_NINO", regexp.MustCompile(`\b[A-CEGHJ-PR-TW-Z][A-CEGHJ-NPR-TW-Z] ?\d{2} ?\d{2} ?\d{2} ?[A-D]\b`), nil},
	{"IN_PAN", regexp.MustCompile(`\b[A-Z]{3}[ABCFGHJLPT][A-Z]\d{4}[A-Z]\b`), nil},
	{"SG_NRIC_FIN", regexp.MustCompile(`\b[STFGM]\d{7}[A-Z]\b`), nil},
	{"US_ITIN", regexp.MustCompile(`\b9\d{2}[- ]?(?:5\d|6[0-5]|7\d|8[0-8]|9[0-24-9])[- ]?\d{4}\b`), nil},
	{"US_SSN", regexp.MustCompile(`\b\d{3}[- ]\d{2}[- ]\d{4}\b`), validSSN},
	{"UK_NHS", regexp.MustCompile(`\b\d{3}[ -]?\d{3}[ -]?\d{4}\b`), validNHS},
	{"AU_MEDICARE", regexp.MustCompile(`\b[2-6]\d{3} ?\d{5} ?\d\b`), validMedicare},
	{"AU_ABN", regexp.MustCompile(`\b\d{2} ?\d{3} ?\d{3} ?\d{3}\b`), validABN},
	{"AU_TFN", regexp.MustCompile(`\b\d{3} ?\d{3} ?\d{3}\b`), validTFN},
	{"AU_ACN", regexp.MustCompile(`\b\d{3} ?\d{3} ?\d{3}\b`), validACN},
	{"PL_PESEL", regexp.MustCompile(`\b\d{11}\b`), validPESEL},
	{"IN_AADHAAR", regexp.MustCompile(`\b[2-9]\d{3} ?\d{4} ?\d{4}\b`), nil},
	{"DATE_TIME", regexp.MustCompile(`\b(?:19|20)\d{2}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])(?:[T ]\d{2}:\d{2}(?::\d{2})?)?\b|\b(?:0?[1-9]|[12]\d|3[01])[/.-](?:0?[1-9]|[12]\d|3[01])[/.-](?:19|20)\d{2}\b`), nil},
	{"PHONE_NUMBER", regexp.MustCompile(`(?:\+\d{1,3}[\s.-]?)?(?:\(\d{2,4}\)|\b\d{2,4})[\s.-]?\d{3,4}[\s.-]?\d{3,4}\b`), validPhone},
	{"LOCATION", regexp.MustCompile(`\b\d{1,5}[A-Za-z]?\s+(?:[A-Z][a-z]+\s+){1,4}(?:Street|St|Avenue|Ave|Road|Rd|Boulevard|Blvd|Drive|Dr|Lane|Ln|Place|Pl|Court|Ct|Way|Highway|Hwy)\b`), nil},
	{"IN_VEHICLE_REGISTRATION", regexp.MustCompile(`\b[A-Z]{2}[ -]?\d{1,2}[ -]?[A-Z]{1,3}[ -]?\d{4}\b`), nil},
	{"IN_VOTER", regexp.MustCompile(`\b[A-Z]{3}\d{7}\b`), nil},
	{"SG_UEN", regexp.MustCompile(`\b\d{8,9}[A-Z]\b|\b[TSR]\d{2}[A-Z]{2}\d{4}[A-Z]\b`), nil},
	{"IT_IDENTITY_CARD", regexp.MustCompile(`\bC[A-Z]\d{5}[A-Z]{2}\b`), nil},
	{"IT_DRIVER_LICENSE", regexp.MustCompile(`\b[A-Z]{2}\d{7}[A-Z]\b`), nil},
	{"IT_PASSPORT", regexp.MustCompile(`\b[A-Z]{2}\d{7}\b`), nil},
	{"MEDICAL_LICENSE", regexp.MustCompile(`\b[A-Z]{2}\d{6}\b`), nil},
	{"US_PASSPORT", regexp.MustCompile(`\b[A-Z]\d{8}\b`), nil},
	{"US_DRIVER_LICENSE", regexp.MustCompile(`\b[A-Z]\d{7}\b`), nil},
	{"IN_PASSPORT", regexp.MustCompile(`\b[A-Z]\d{7}\b`), nil},
	{"US_BANK_NUMBER", regexp.MustCompile(`\b\d{8,17}\b`), nil},
}

// PIIEntityNames lists every supported entity name (n8n's names).
func PIIEntityNames() []string {
	out := make([]string, len(piiEntities))
	for i, e := range piiEntities {
		out[i] = e.name
	}
	return out
}

func knownEntity(name string) bool {
	for _, e := range piiEntities {
		if e.name == name {
			return true
		}
	}
	return false
}

// finding is one detected value: its type (entity or regex name) and text.
type finding struct {
	kind, text string
	start, end int
}

// spanSet records claimed byte ranges of the text.
type spanSet [][2]int

func (s *spanSet) overlaps(a, b int) bool {
	for _, r := range *s {
		if a < r[1] && r[0] < b {
			return true
		}
	}
	return false
}

func (s *spanSet) add(a, b int) { *s = append(*s, [2]int{a, b}) }

// detectPII finds the selected entities (all when selected is empty).
func detectPII(text string, selected []string) []finding {
	want := map[string]bool{}
	for _, s := range selected {
		want[s] = true
	}
	var claimed spanSet
	var out []finding
	for _, e := range piiEntities {
		if len(want) > 0 && !want[e.name] {
			continue
		}
		for _, m := range e.re.FindAllStringIndex(text, -1) {
			v := text[m[0]:m[1]]
			if claimed.overlaps(m[0], m[1]) || (e.valid != nil && !e.valid(v)) {
				continue
			}
			claimed.add(m[0], m[1])
			out = append(out, finding{kind: e.name, text: v, start: m[0], end: m[1]})
		}
	}
	return out
}

func digitsOf(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func validCard(s string) bool {
	d := digitsOf(s)
	if len(d) < 13 || len(d) > 19 {
		return false
	}
	sum, double := 0, false
	for i := len(d) - 1; i >= 0; i-- {
		n := int(d[i] - '0')
		if double {
			if n *= 2; n > 9 {
				n -= 9
			}
		}
		sum += n
		double = !double
	}
	return sum%10 == 0
}

func validIBAN(s string) bool {
	c := strings.ReplaceAll(s, " ", "")
	if len(c) < 15 || len(c) > 34 {
		return false
	}
	c = c[4:] + c[:4]
	var num strings.Builder
	for _, r := range c {
		switch {
		case r >= '0' && r <= '9':
			num.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			num.WriteString(strconv.Itoa(int(r-'A') + 10))
		default:
			return false
		}
	}
	n, ok := new(big.Int).SetString(num.String(), 10)
	return ok && new(big.Int).Mod(n, big.NewInt(97)).Int64() == 1
}

func validIP(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return false
	}
	if strings.Contains(s, ":") {
		// An IPv6 address, not a time of day: at least a few hex digits.
		return len(strings.Map(func(r rune) rune {
			if r == ':' {
				return -1
			}
			return r
		}, s)) >= 4
	}
	return true
}

func validSSN(s string) bool {
	d := digitsOf(s)
	area, group, serial := d[:3], d[3:5], d[5:]
	return area != "000" && area != "666" && area[0] != '9' && group != "00" && serial != "0000"
}

func validNHS(s string) bool {
	d := digitsOf(s)
	sum := 0
	for i := 0; i < 9; i++ {
		sum += int(d[i]-'0') * (10 - i)
	}
	check := 11 - sum%11
	if check == 11 {
		check = 0
	}
	return check != 10 && check == int(d[9]-'0')
}

const dniLetters = "TRWAGMYFPDXBNJZSQVHLCKE"

func validNIF(s string) bool {
	d := digitsOf(s)
	n, _ := strconv.Atoi(d)
	return dniLetters[n%23] == s[len(s)-1]
}

func validNIE(s string) bool {
	prefix := strings.IndexByte("XYZ", s[0])
	n, _ := strconv.Atoi(strconv.Itoa(prefix) + digitsOf(s))
	return dniLetters[n%23] == s[len(s)-1]
}

func validHETU(s string) bool {
	n, _ := strconv.Atoi(s[:6] + s[7:10])
	return "0123456789ABCDEFHJKLMNPRSTUVWXY"[n%31] == s[10]
}

func weighted(d string, weights []int) int {
	sum := 0
	for i, w := range weights {
		sum += int(d[i]-'0') * w
	}
	return sum
}

func validPESEL(s string) bool {
	check := (10 - weighted(s, []int{1, 3, 7, 9, 1, 3, 7, 9, 1, 3})%10) % 10
	return check == int(s[10]-'0')
}

func validMedicare(s string) bool {
	d := digitsOf(s)
	return weighted(d, []int{1, 3, 7, 9, 1, 3, 7, 9})%10 == int(d[8]-'0')
}

func validABN(s string) bool {
	d := []byte(digitsOf(s))
	d[0]--
	return weighted(string(d), []int{10, 1, 3, 5, 7, 9, 11, 13, 15, 17, 19})%89 == 0 && d[0] >= '0'
}

func validTFN(s string) bool {
	return weighted(digitsOf(s), []int{1, 4, 3, 7, 5, 8, 6, 9, 10})%11 == 0
}

func validACN(s string) bool {
	d := digitsOf(s)
	check := (10 - weighted(d, []int{8, 7, 6, 5, 4, 3, 2, 1})%10) % 10
	return check == int(d[8]-'0')
}

func validPhone(s string) bool {
	n := len(digitsOf(s))
	return n >= 9 && n <= 15
}
