package vector

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Lengths in the character splitters are counted in runes (Unicode code
// points), not bytes, so a chunk_size of 1000 means 1000 characters of
// any script.

// splitCharacter splits on one separator and packs the pieces into chunks
// of at most size runes, carrying up to overlap runes of trailing pieces
// into the next chunk. A single piece longer than size becomes its own
// (oversized) chunk — this splitter never cuts inside a piece.
func splitCharacter(text, sep string, size, overlap int) []string {
	return mergePieces(splitKeepNonEmpty(text, sep), sep, size, overlap)
}

// splitRecursive tries separators in order: it splits on the first one
// present in the text, keeps pieces that fit, and recurses into oversized
// pieces with the remaining separators. An empty separator splits into
// single characters, so with the default list no chunk exceeds size.
func splitRecursive(text string, seps []string, size, overlap int) []string {
	sep := ""
	rest := []string(nil)
	for i, s := range seps {
		if s == "" || strings.Contains(text, s) {
			sep, rest = s, seps[i+1:]
			break
		}
		if i == len(seps)-1 {
			sep = s
		}
	}
	var out, fitting []string
	for _, piece := range splitKeepNonEmpty(text, sep) {
		if runeLen(piece) <= size {
			fitting = append(fitting, piece)
			continue
		}
		if len(fitting) > 0 {
			out = append(out, mergePieces(fitting, sep, size, overlap)...)
			fitting = nil
		}
		if len(rest) == 0 {
			out = append(out, piece)
		} else {
			out = append(out, splitRecursive(piece, rest, size, overlap)...)
		}
	}
	if len(fitting) > 0 {
		out = append(out, mergePieces(fitting, sep, size, overlap)...)
	}
	return out
}

func splitKeepNonEmpty(text, sep string) []string {
	var parts []string
	if sep == "" {
		parts = strings.Split(text, "")
	} else {
		parts = strings.Split(text, sep)
	}
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// mergePieces joins pieces with sep into chunks no longer than size (unless
// a single piece is), then starts the next chunk with the trailing pieces
// of the previous one whose combined length stays within overlap.
func mergePieces(pieces []string, sep string, size, overlap int) []string {
	sepLen := runeLen(sep)
	var chunks []string
	var window []string
	total := 0 // rune length of strings.Join(window, sep)

	joinedLen := func(add int) int {
		if len(window) == 0 {
			return add
		}
		return total + sepLen + add
	}
	for _, p := range pieces {
		pl := runeLen(p)
		if len(window) > 0 && joinedLen(pl) > size {
			if c := strings.TrimSpace(strings.Join(window, sep)); c != "" {
				chunks = append(chunks, c)
			}
			// Drop leading pieces until the remainder fits the overlap
			// budget and leaves room for the new piece.
			for len(window) > 0 && (total > overlap || joinedLen(pl) > size) {
				drop := runeLen(window[0])
				if len(window) > 1 {
					drop += sepLen
				}
				total -= drop
				window = window[1:]
			}
		}
		total = joinedLen(pl)
		window = append(window, p)
	}
	if c := strings.TrimSpace(strings.Join(window, sep)); c != "" {
		chunks = append(chunks, c)
	}
	return chunks
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// ── approximate tokens ──────────────────────────────────────────────────────

// tokenPiece mimics a BPE pre-tokenizer: a run of letters, up to three
// digits, or a run of punctuation, each with its leading whitespace; or a
// run of whitespace on its own.
var tokenPiece = regexp.MustCompile(`\s*[\p{L}\p{M}]+|\s*\p{N}{1,3}|\s*[^\s\p{L}\p{M}\p{N}]+|\s+`)

// approxTokenRunes is the letter count above which a word is cut into
// several tokens — English BPE averages about four characters per token.
const approxTokenRunes = 4

// approxTokens splits text into approximate tokens. There is no tokenizer
// dependency: counts track OpenAI's cl100k encoding to within roughly ±20%
// on English prose and drift further on code and non-Latin scripts.
// Concatenating the result reproduces text exactly.
func approxTokens(text string) []string {
	var toks []string
	for _, p := range tokenPiece.FindAllString(text, -1) {
		trimmed := strings.TrimLeft(p, " \t\r\n")
		lead := p[:len(p)-len(trimmed)]
		r := []rune(trimmed)
		if len(r) <= approxTokenRunes {
			toks = append(toks, p)
			continue
		}
		for i := 0; i < len(r); i += approxTokenRunes {
			end := i + approxTokenRunes
			if end > len(r) {
				end = len(r)
			}
			seg := string(r[i:end])
			if i == 0 {
				seg = lead + seg
			}
			toks = append(toks, seg)
		}
	}
	return toks
}

// splitTokens slides a window of size approximate tokens over the text,
// advancing by size-overlap tokens each step.
func splitTokens(text string, size, overlap int) []string {
	toks := approxTokens(text)
	if len(toks) == 0 {
		return nil
	}
	step := size - overlap
	var out []string
	for start := 0; start < len(toks); start += step {
		end := start + size
		if end > len(toks) {
			end = len(toks)
		}
		if c := strings.Join(toks[start:end], ""); strings.TrimSpace(c) != "" {
			out = append(out, c)
		}
		if end == len(toks) {
			break
		}
	}
	return out
}

// unescapeSeparator turns the two-character sequences \n, \t and \r typed
// into a form field into the control characters they name.
func unescapeSeparator(s string) string {
	return strings.NewReplacer(`\n`, "\n", `\t`, "\t", `\r`, "\r").Replace(s)
}
