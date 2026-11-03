// Package novelty decides whether a proposed direction is new. Normalise turns a title into a stable key, and Checker
// compares it with prior directions of the run, memory and knowledge through the narrow Corpus interface. It is a plain
// library: no init, no goroutine, no file or network access; every lookup is a call the caller's Corpus makes.
package novelty

import (
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// Bounds on the normalisation work. The input is refused, never truncated, when it is over the cap, so two different long
// titles can never collapse into one key by being cut at the same place.
const (
	MaxInputBytes = 4 * types.MaxTitleLen // 200 characters of up to four bytes
	MaxTokens     = 64
	MaxTokenRunes = 64
)

var (
	ErrTooLong  = errors.New("novelty: title over the input cap")
	ErrEmptyKey = errors.New("novelty: title has no significant words")
)

var stopwords = buildStopwords()

func buildStopwords() map[string]struct{} {
	m := map[string]struct{}{}
	for _, w := range strings.Fields(`a an the and or of to in on for with by at from as is are be this that it its into via
		about over under using use new add make do does please we our you your all any some`) {
		m[w] = struct{}{}
	}
	return m
}

// Normalise reduces a title to its key text: compatibility-decomposed (full-width and accented forms fold to plain
// ones), marks removed, lower-cased, split on everything that is not a letter or digit, stopwords dropped, the remaining
// words de-duplicated and sorted. The same words in any case, spacing, punctuation, script width or order give the same
// text. Work is bounded by MaxInputBytes.
func Normalise(title string) (string, error) {
	if len(title) > MaxInputBytes {
		return "", ErrTooLong
	}
	if !utf8.ValidString(title) {
		title = strings.ToValidUTF8(title, " ")
	}
	folded := norm.NFKD.String(title)
	words := strings.FieldsFunc(folded, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r) })
	seen := make(map[string]struct{}, len(words))
	var out []string
	for _, w := range words {
		w = strings.Map(func(r rune) rune {
			if unicode.IsMark(r) {
				return -1
			}
			return unicode.ToLower(r)
		}, w)
		if w == "" {
			continue
		}
		if utf8.RuneCountInString(w) > MaxTokenRunes {
			w = string([]rune(w)[:MaxTokenRunes])
		}
		if _, stop := stopwords[w]; stop {
			continue
		}
		if _, dup := seen[w]; dup {
			continue
		}
		seen[w] = struct{}{}
		out = append(out, w)
	}
	if len(out) == 0 {
		return "", ErrEmptyKey
	}
	sort.Strings(out)
	if len(out) > MaxTokens {
		out = out[:MaxTokens]
	}
	return strings.Join(out, " "), nil
}

// Key is the normalised text and its hash, the value stored as Direction.KeyHash.
func Key(title string) (string, [32]byte, error) {
	n, err := Normalise(title)
	if err != nil {
		return "", [32]byte{}, err
	}
	return n, types.KeyHash(n), nil
}
