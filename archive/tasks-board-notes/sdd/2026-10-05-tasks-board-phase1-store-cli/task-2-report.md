# Task 2 report: types, errors and text cleaning

Status: DONE after fix round 1 (it was DONE_WITH_CONCERNS for the first two commits)
Commits (on feat/tasks-board, on top of 735ca584):
- `c7b54bde feat(tasks): types, errors and text cleaning`: the brief's Step 6 commit. Code verbatim; one assertion added to a test (deviation 1).
- `4ed03708 fix(tasks): cleanURL refuses a URL with control, hidden or invalid characters anywhere`: the hardening the controller approved (deviation 2).
- `62fee95e fix(tasks): cutBytes stays within its limit and is stable under a second cut; the cleaning tests pin C1 controls, range ends, joiners and every cap`: fix round 1 (the last section).

Implementer: p1-t2-impl

## What I implemented

- `internal/tasks/model.go`: exactly the brief's code (statuses, `ParseStatus`, source constants, `ActorKind`/`Actor.Label`, the limit constants, `Source`, `Claim`, `LastEvent`, `Task`, `Event`, `Profile`, `Counts`, `Board`, `Filter`, `AddInput`, `Edit`, `Placement`, `Outcome`, the seven errors and their constructors, `*ClaimedError`).
- `internal/tasks/clean.go`: the brief's code (`nameRE`, `clientIDRE`, `hidden`, `cleanText`, `oneLine`, `cutRunes`, `cutBytes`, `cleanTitle`, `deriveTitleNotes`, `cleanURL`); `cleanURL` hardened in the second commit.
- `internal/tasks/clean_test.go`: the brief's seven tests, plus the additions of deviations 1 and 2.
- Did not touch `doc.go` or `schema_test.go`; no name is redeclared. No docs touched.

## Deviations from the brief

1. **One assertion added to `TestCutBytesCutsOnACharacterAndSaysSo`** (commit 1, clean_test.go lines 61 to 65). The brief's test cuts two-byte characters at 65536 bytes, an even number, so the cut already lands on a character boundary and the back-up loop in `cutBytes` never runs. Proved by mutation (commit 1 table, rows 4 and 5): deleting the loop left every test green, so the test asserted nothing about "a cut never splits a character". The added case is `"x" + 20000 four-byte characters`: the limit then falls three bytes into a character, and the expected result is exactly `"x" + 16383 characters + marker (20001 characters in the original)`. It also fails if the loop becomes a single `if` step. Code unchanged; only the test was fixed.
2. **`cleanURL` hardened (commit 2), as approved after my question.** After the length check, `cleanURL` returns `""` when `!utf8.ValidString(raw)` or any rune of `raw` is `unicode.IsControl` or `hidden`. It refuses, it does not re-encode, so a link is never changed. Differences from the first patch I proposed: the check is on the raw input (as approved), not on the URL that would be stored, so a hidden character in a path, fragment or host (which `net/url` would have percent-encoded, safely) is now refused too, which is the "anywhere" of the subject. I added one test row beyond the ones you listed, a bidi override in a path, to pin that (mutation D below shows it is the only row that tells the two variants apart). I also extended the doc comment of `cleanURL` by three lines (what is refused, and why: `net/url` keeps a query exactly as typed).
3. **No compile slips in the plan's code.** `model.go` and `clean.go` compiled, vetted and formatted as written.
4. **Escapes.** The brief's invisible test characters are in the file as the brief writes them (`\u202e`, `\u2066`, `\ufeff`, `\ufffd`, `\u200d` twice, `\U000E0049`, `\U000E0067`, the `\U0001F4..` family), but only after a repair (Concerns 2). The new rows of commit 2 use 8-digit and `\x` escapes. I did not rewrite the brief's existing 4-digit escapes in clean_test.go (your option): it would have mixed unrelated churn into a fix commit; they are intact (grep below).

## Testing

All of these were run on the tree after commit 2 (clean, equal to HEAD):

| Command | Result |
|---|---|
| `go test ./internal/tasks/ -count=1 -v` | 12 of 12 top-level tests pass: the 7 new ones and the 5 of Task 1's `schema_test.go`; `ok ... 0.256s` |
| `go test ./internal/tasks/ -count=1` (the last run of all) | `ok ... 0.304s` |
| `gofmt -l internal/tasks` | prints nothing |
| `go vet ./internal/tasks/` | ok |
| `git status --short` | prints nothing (the report is ignored by git) |
| `grep -noP '[^\x00-\x7F]+'` over clean_test.go, clean.go, model.go | only visible characters: clean_test.go 37 `é`, `ö`, `日本語`; 39 `…`; 49 `é`; 111 `…`; 135 `é` and `日本語` twice; clean.go 59 `…`; model.go none. A per-character scan (category of each) finds no format, control or space-separator character in any of the three files. Same list in the committed blobs |
| `LC_ALL=C sed -n '175,457p' task-2-brief.md \| diff - internal/tasks/model.go`, `'463,583p'` against `clean.go`, `'16,164p'` against `clean_test.go` (run on the tree of commit 1) | model.go and clean.go: no difference. clean_test.go: only the five lines of deviation 1 (`60a61,65`). model.go is still identical at HEAD; clean.go and clean_test.go differ from the brief by commit 2 as well |
| throwaway probe of model.go through `go test -overlay` (not committed, worktree untouched) | passes: `ParseStatus` (case, space, `-`, `progress`, three refusals), `IsBoard`, `Label` for all five actor shapes, `ClaimedError` with `errors.Is`, `errors.As` and its message, `Placement.set`, `validSource`, the five error constructors and the limit constants |

model.go has no tests in this task's brief; the probe above is the only check beyond compilation, and later tasks' tests will exercise it.

## TDD evidence

### Commit 1 (the brief)

**RED, first run: a setup failure, not the expected RED.**
`go test ./internal/tasks/ -run 'TestClean|TestCut|TestDerive|TestADerived|TestName' -count=1`
```
internal/tasks/clean_test.go:18:24: illegal byte order mark
FAIL	github.com/monoes/mono-agent/internal/tasks [setup failed]
```
Cause: my Write call turned the 4-digit escapes in the test code into raw characters (Concerns 2). Repaired with a script, then re-ran.

**RED, second run (the expected one):** same command.
```
internal/tasks/clean_test.go:25:11: undefined: cleanText
internal/tasks/clean_test.go:38:10: undefined: cutRunes
internal/tasks/clean_test.go:50:9: undefined: cutBytes
internal/tasks/clean_test.go:50:24: undefined: MaxNotesBytes
internal/tasks/clean_test.go:86:25: undefined: deriveTitleNotes
internal/tasks/clean_test.go:88:24: undefined: ErrInvalid
... too many errors
FAIL	github.com/monoes/mono-agent/internal/tasks [build failed]
```
Expected: neither `model.go` nor `clean.go` existed.

**GREEN:** same command with `-v`: all of `TestCleanText` (10 subtests), `TestCutRunesNeverSplitsACharacter`, `TestCutBytesCutsOnACharacterAndSaysSo`, `TestDeriveTitleNotes` (10 subtests), `TestADerivedTitleIsCutAtOneHundredTwentyCharacters`, `TestCleanURL`, `TestNameAndClientIDShapes` PASS; `ok ... 0.368s`.

**Mutation sanity check** (temporary edits to clean.go; each one undone, and `cmp` showed clean.go byte-identical to a pristine copy taken before the first one):

| # | Mutation | Result |
|---|---|---|
| 1 | the bidi case of `hidden` returns false | FAIL `TestCleanText/bidi_overrides`: `cleanText("a\u202eb\u2066c") = "a\u202eb\u2066c", want "abc"`; nothing else fails |
| 2 | the tag-block case returns false | FAIL `TestCleanText/unicode_tag_characters`: `cleanText("visible\U000e0049\U000e0067hidden") = (unchanged), want "visiblehidden"` |
| 3 | `return r == 0xFEFF` becomes `return false` | FAIL `TestCleanText/byte_order_mark` |
| 4 | delete the `for !utf8.ValidString(cut)` loop in `cutBytes`, brief's test | **PASS**: the test asserted nothing about the boundary. Fixed the test (deviation 1) |
| 4b | same mutation, with the added assertion | FAIL `a cut inside a four-byte character: 65582 bytes, valid UTF-8 false, want 65579 bytes` |
| 5 | the loop becomes a single `if` step | FAIL `... 65581 bytes, valid UTF-8 false, want 65579 bytes` |

### Commit 2 (the `cleanURL` hardening)

Why: probe against the brief's `cleanURL`. `url.Parse` keeps the raw query as typed and `u.String()` writes it back unchanged, so in `https://example.com/?q=a<char>b` these survived: U+202E (bidi override), U+E0049 (tag character), U+FEFF, U+009B (C1 control: a one-character escape introducer in a UTF-8 terminal) and an invalid byte `\xff`. In a path, fragment or host the same characters came out percent-encoded, `url.Parse` already refuses ASCII controls, and one in user-info fails to parse.

**RED** (rows added to `TestCleanURL` first, against the unhardened `cleanURL`; the accented and CJK row passes already, it is the guard against over-refusing):
`go test ./internal/tasks/ -run 'TestCleanURL' -count=1`
```
--- FAIL: TestCleanURL (0.00s)
    clean_test.go:141: cleanURL("https://example.com/?q=a\u202eb") = "https://example.com/?q=a\u202eb", want ""
    clean_test.go:141: cleanURL("https://example.com/?q=a\U000e0049b") = "https://example.com/?q=a\U000e0049b", want ""
    clean_test.go:141: cleanURL("https://example.com/?q=a\u009b31mb") = "https://example.com/?q=a\u009b31mb", want ""
    clean_test.go:141: cleanURL("https://example.com/?q=a\xffb") = "https://example.com/?q=a\xffb", want ""
    clean_test.go:141: cleanURL("https://example.com/a\u202eb") = "https://example.com/a%E2%80%AEb", want ""
FAIL
```
Expected: the check did not exist.

**GREEN:** same command with `-v` after the fix: `--- PASS: TestCleanURL`, `ok ... 0.257s`; then the whole package (above).

The change (12 insertions, 1 deletion):
```diff
-// user-info and within the limit; otherwise "".
+// user-info and within the limit; otherwise "". A URL with invalid UTF-8, a
+// control character or a hidden character anywhere is refused, not rewritten:
+// net/url keeps a query exactly as typed.
 func cleanURL(raw string) string {
 	raw = strings.TrimSpace(raw)
 	if raw == "" || len(raw) > MaxURLBytes {
 		return ""
 	}
+	if !utf8.ValidString(raw) || strings.ContainsFunc(raw, func(r rune) bool { return unicode.IsControl(r) || hidden(r) }) {
+		return ""
+	}
 	u, err := url.Parse(raw)
```
and in `TestCleanURL`, after the `\x00` row: a query with a bidi override, a tag character, U+009B and `\xff` (each wanting `""`), a path with a bidi override (wanting `""`), and `https://example.com/?q=café&lang=日本語` (wanting itself).

**Mutation sanity check of the fix** (temporary edits; after the last one `cmp` showed clean.go and clean_test.go byte-identical to copies saved before the first):

| # | Mutation | Result |
|---|---|---|
| A | drop `!utf8.ValidString(raw) \|\|` | FAIL only the `\xff` row |
| B | keep only `hidden(r)` (no `unicode.IsControl`) | FAIL only the U+009B row (the NUL row is still refused by `url.Parse`) |
| C | keep only `unicode.IsControl(r)` (no `hidden`) | FAIL the bidi, tag and path rows |
| D | check the URL that would be stored instead of `raw` (my first proposal) | FAIL only the path row |

## Files changed

- Commit 1: `internal/tasks/model.go`, `internal/tasks/clean.go`, `internal/tasks/clean_test.go` (all new).
- Commit 2: `internal/tasks/clean.go`, `internal/tasks/clean_test.go`.
- Working tree is clean and equals HEAD.

## Self-review

- Commit 1 compared with the brief mechanically (the `diff` commands in the Testing table); commit 2's full diff read before committing: one condition, a three-line comment, six test rows, nothing else.
- No stray files, debug prints or commented-out code in the tree. Scratch files live in the scratchpad only.
- Nit, kept as in the brief: the parameter `max` of `cutBytes` shadows the Go 1.21 builtin. Harmless; vet is clean.
- `timeFmt`, `positionGap`, `Placement.set`, `validSource` are unused until later tasks. CI runs `go vet` only (no staticcheck or golangci config), so nothing flags them.
- Test output is pristine (no stray lines).

## Concerns

1. **(Resolved by commit 2.)** The `cleanURL` gap is closed; see deviation 2 and the commit 2 evidence.
2. **The tool transport rewrites 4-digit unicode escapes: you are carrying this into the later dispatches; for the record.** My Write call turned each 4-digit escape in the test code into the raw character (U+202E, U+2066, U+FEFF, U+FFFD, U+200D), while the 8-digit and `\x` forms survived; it was not even consistent (an Edit kept `\u009b` but decoded `\u202e`). Only the BOM stopped the compiler ("illegal byte order mark"); a raw bidi override, ZWJ or U+FFFD compiles silently, so the grep is the real guard. A raw U+FFFD also appeared in `clean.go`. Repair used on every file I wrote (it turns each raw invisible, format or control character and U+FFFD back into an escape and lists what it changed): `python3 /private/tmp/claude-501/-Users-morteza-Desktop-monoes-mono-agent--claude-worktrees-feat-openai-compatible-api/54d60522-f555-44a3-802d-b3c4ca92ce82/scratchpad/t2-escape-raw.py FILE`, then the grep. It also applies to this report: the same repair was needed on it.
3. **Residual points, all as the brief defines them (nothing to change in this task, for your judgment):**
   - A title made only of an invisible character that is not in `hidden` is accepted as non-empty. Probe (a copy of the committed `hidden` and `cleanText`): U+200B, U+2060, U+2800 (braille blank), U+3164 (Hangul filler), U+200E (LRM), U+061C and U+E0100 (variation selector supplement) all survive; NBSP, U+3000 and U+2028 count as space. Review Focus 1 as written ("spaces, control characters only") holds. The same list is not refused in a URL either.
   - `cutBytes` keeps at most `MaxNotesBytes` bytes of text and then adds the marker, so stored notes can be up to about 46 bytes over 64 KiB (the test pins the marker to start within the limit).
   - `cutRunes` cuts between code points, not grapheme clusters, so a cut can leave a dangling ZWJ or a base letter without its combining mark (cosmetic; the cut text is always valid UTF-8 and within the limit). The test's ZWJ input checks validity and length only.

## Fix round 1

Status after this round: DONE (the notes for the controller at the end are non-blocking).
Commit: `62fee95e fix(tasks): cutBytes stays within its limit and is stable under a second cut; the cleaning tests pin C1 controls, range ends, joiners and every cap` (2 files, 190 insertions, 20 deletions), on top of the spec commit de879f23. It supersedes two points above: the `max` nit of the self-review and the second residual bullet of Concerns 3 (stored notes up to about 46 bytes over 64 KiB). Both are fixed here.

### Changes

Production, `internal/tasks/clean.go` (132 lines now):
- `cutBytes(s string, maxBytes int)` (Important 1). It builds the notice first, keeps `max(maxBytes-len(notice), 0)` bytes cut back to a character boundary, and appends the notice. The result is within `maxBytes`, and cutting a cut text changes nothing. Only a limit shorter than the notice itself gives more than `maxBytes` (the notice alone); the doc comment says so. The parameter is renamed from `max`, so the builtin works inside the function.
- `deriveTitleNotes` (Minor 3b). The first case of the switch refuses notes and text given together: `invalid("give notes or text, not both")`. It runs on the cleaned values, so notes that clean to nothing are not notes. The doc comment says so.
- The comment on `hidden` says "bidi controls" (Minor 7).

Tests, `internal/tasks/clean_test.go` (324 lines now). A helper `checkCut(t, got, limit)` asserts what every cut text owes: valid UTF-8, within the limit, `cutBytes(got, limit) == got`, and `cutBytes(cleanText(got), limit) == got` (the expression Task 5's `Edit` runs on re-sent notes).

### Covering tests, group A: red on the old code (behaviour changes)

1. `TestCutBytesCutsOnACharacterAndSaysSo`: `checkCut` on the brief's long input; the four-byte case retuned. The input is `"xxx"` and 20000 four-byte characters: the notice takes 46 bytes, the budget is 65490, and the budget falls three bytes into a character, so the cut steps back three times. A premise assertion fails the test if the input ever stops putting the limit inside a character (the brief's own test lost its point that way). The expected string is exact, then `checkCut`.
2. `TestCutBytesKeepsExactlyTheLimitAndCutsOneByteMore` (new): 65536 bytes unchanged; 65537 cut to exactly 65536 bytes (65490 of text and the notice).
3. `TestCutBytesWithALimitShorterThanTheNoticeGivesTheNoticeAlone` (new): pins the clamp at zero (without it the slice panics).
4. `TestDeriveTitleNotes`: the two new rows `title, notes and text...` and `notes and text without a title` (3b). The error column is now a message substring (3a), so `notes without a title` asserts "need a title" ("needs a title or some text" does not contain it). The brief's row `title and text: the text is the notes` is the third row you named (title and text, no notes, still makes the text the notes); it is kept as it is, not duplicated.
5. `TestDeriveTitleNotesCutsNotesAndTextAtSixtyFourKiB` (new, 3 subtests): notes over 64 KiB with a title; text over 64 KiB with a title (the text becomes the notes); text over 64 KiB alone. Each wants the exact notes (65490 bytes of text, the notice, 65536 in all) and `checkCut`.

RED, the new tests against the old production code (`go test ./internal/tasks/ -count=1`; line numbers are those of the file at that run):
```
--- FAIL: TestCutBytesCutsOnACharacterAndSaysSo (0.00s)
    clean_test.go:103: the cut text is 65582 bytes, over the limit of 65536
    clean_test.go:103: cutting the cut text again changed it: 65582 bytes became 65582
    clean_test.go:103: cutting the cleaned cut text again changed it: 65582 bytes became 65582
    clean_test.go:113: a cut inside a four-byte character: 65581 bytes, valid UTF-8 true, want 65533 bytes
    clean_test.go:115: the cut text is 65581 bytes, over the limit of 65536
    clean_test.go:115: cutting the cut text again changed it: 65581 bytes became 65582
--- FAIL: TestCutBytesKeepsExactlyTheLimitAndCutsOneByteMore (0.00s)
    clean_test.go:128: a text of 65537 bytes: 65582 bytes, want 65536
--- FAIL: TestCutBytesWithALimitShorterThanTheNoticeGivesTheNoticeAlone (0.00s)
    clean_test.go:136: got "aaaaaaaaaa\n[truncated: 100 characters in the original]", want "\n[truncated: 100 characters in the original]"
--- FAIL: TestDeriveTitleNotes (0.00s)
    --- FAIL: TestDeriveTitleNotes/title,_notes_and_text:_no_words_are_dropped_silently (0.00s)
        clean_test.go:165: err <nil>, want ErrInvalid containing "not both"
    --- FAIL: TestDeriveTitleNotes/notes_and_text_without_a_title (0.00s)
        clean_test.go:165: err invalid input: notes need a title: give a title, or send the text alone and its first line becomes the title, want ErrInvalid containing "not both"
--- FAIL: TestDeriveTitleNotesCutsNotesAndTextAtSixtyFourKiB (0.00s)
    --- FAIL: .../notes_over_the_limit, .../text_over_the_limit_becomes_the_notes, .../text_alone_over_the_limit
        clean_test.go:225: got ("T", 65582 bytes of notes, <nil>), want title "T" and 65536 bytes of notes
```
Expected: the old `cutBytes` keeps `max` bytes and then adds the notice (46 bytes over), and the old `deriveTitleNotes` drops the text when a title and notes are given. Every other test of the package passed on the old code at that point.

### Covering tests, group B: green on the old code, each killed by a named mutant (coverage rows)

A green run on the old code does not mean "asserts nothing" here: these rows pin rules the old code already had, and the mutation table below names the mutant each one kills.
- `TestCleanText`, rows `c1 controls` (Important 2), `range ends`, `joiners stay` (with the comment about Persian and emoji) and `invalid run` (Minor 1), all with 8-digit escapes.
- `TestCutRunesKeepsExactlyTheLimitAndCutsOneRuneMore` (new): 10 and 11 runes, 10 and 11 two-byte runes, and the space before the ellipsis.
- `TestATitleIsKeptAtTwoHundredCharactersAndCutBeyond` (new): 200 and 201 characters through `cleanTitle`.
- `TestADerivedTitleOfOneHundredTwentyCharactersIsKeptAndOneMoreIsCut` (new): 120 and 121 characters.
- `TestCleanURL`: the row `multibyte` (700 U+00E9 characters: 1,420 bytes typed, over 2,048 once escaped), with a premise assertion that it passes the first length check, so only the second one refuses it (Minor 5).
- `TestParseStatus` and `TestActorLabel` (new, Minor 4): all six names, the aliases `progress` and `in-progress`, case, surrounding space, two refusals; the five shapes of `Label`.
- `TestCutRunesNeverSplitsACharacter` is renamed `TestCutRunesGivesValidUTF8WithinTheLimitEndingInAnEllipsis` (Minor 6).
- Inputs are the spec's literal numbers (200 and 201, 120 and 121, 65536 and 65537, `64<<10`) with the limit passed as the constant, so a cap that drifts either way fails a row.

### Commands, on the committed tree

```
gofmt -l internal/tasks                       (prints nothing)
go vet ./internal/tasks/                      (ok)
go test ./internal/tasks/ -count=1 -v         20 of 20 top-level tests PASS (15 in clean_test.go, 5 from schema_test.go); ok ... 0.297s
go test ./internal/tasks/ -count=1            ok ... 0.240s
```
The lead's raw grep, `grep -noP '[^\x00-\x7F]+'` over clean.go, clean_test.go and model.go: only visible characters (clean.go 59 the ellipsis; clean_test.go the letters é and ö, the CJK `日本語`, and the ellipsis; model.go none). A per-character scan (category of each) finds no format, control or space-separator character in any of the three files. The repair script (`t2-escape-raw.py`) and the grep ran after every write: the Go files needed no repair this round (every new invisible character was written as an 8-digit escape).

### Mutation evidence

Run in a scratch module outside the checkout (a copy of clean.go, model.go and clean_test.go; `t2-mutate.py`), so the worktree was never modified and there is nothing to restore in it. Guards: the copies' SHA-256 equal the committed files (clean.go `bc61e29a7cbf5054`, model.go `155349a1b22f9ac3`, clean_test.go `c7ca3734a70de7fd`, first 16 hex digits), checked before the run and again after it; the baseline run passes; every replacement matches exactly once (the C1 mutant targets the `cleanText` case line, not the `cleanURL` condition); a build failure would be reported as invalid, not as a kill (none occurred); each mutant is undone after its run. Result: **39 mutants, 39 killed (1 by a panic), no survivor, no invalid mutant**.

| Item | Mutant | Killed by |
|---|---|---|
| Important 1 | CB1 keeps `maxBytes` of text, notice not reserved | `TestCutBytesCutsOnACharacterAndSaysSo`, `...KeepsExactlyTheLimit...`, `...ALimitShorterThanTheNotice...`, the three `...SixtyFourKiB` subtests |
| | CB2 no clamp at zero | `...ALimitShorterThanTheNotice...` (panic: slice bounds out of range) |
| | CB3 no character-boundary loop; CB4 one step (`if` for `for`); CB6 the notice counts bytes | `TestCutBytesCutsOnACharacterAndSaysSo` |
| | CB5 a text of exactly `maxBytes` is cut | `TestCutBytesCutsOnACharacterAndSaysSo`, `...KeepsExactlyTheLimit...`, the `...SixtyFourKiB` subtests |
| Important 2 | C1 `IsControl` replaced by `r < 0x20` or `r == 0x7f` | `TestCleanText/c1_controls` |
| Minor 1 | R1 U+202A kept; R2 U+2069 kept; R3 U+E0000 kept; R4 U+E007F kept (each range end moved one in) | `TestCleanText/range_ends` |
| | J1 U+200C and U+200D added to `hidden`; J2 `hidden` holds every format character (Cf) | `TestCleanText/joiners_stay` |
| | V1 no `ToValidUTF8` (a run of bad bytes no longer collapses) | `TestCleanText/invalid_run` |
| Minor 2 | T1 `cleanTitle` without its cut; T2 cut at 199; T3 cut at 201 | `TestATitleIsKeptAtTwoHundredCharactersAndCutBeyond` |
| | D1 derived title cut at 100; D2 at 121 | `TestADerivedTitleOfOneHundredTwenty...`, `...SixtyFourKiB/text_alone...` |
| | D3 given notes not cut; D4 text that becomes the notes not cut; D5 text kept as the notes of a derived title not cut | `...SixtyFourKiB/notes_over_the_limit`, `/text_over_the_limit_becomes_the_notes`, `/text_alone_over_the_limit` (one each) |
| | RC1 `cutRunes` cuts exactly n runes; RC2 keeps n runes and the ellipsis; RC3 keeps n-2; RC5 counts bytes | `TestCutRunesKeepsExactlyTheLimit...`, the 120 and 200 tests (RC2 also `TestCutRunesGivesValidUTF8...`) |
| | RC4 keeps the space before the ellipsis | `TestCutRunesKeepsExactlyTheLimit...` (the space row) |
| Minor 3 | N1 refusal removed | `TestDeriveTitleNotes/title,_notes_and_text...`, `/notes_and_text_without_a_title` |
| | N2 `case notes != ""` replaced by `case false` | `TestDeriveTitleNotes/notes_without_a_title` (3a: the message) |
| | N3 refusal only without a title | `TestDeriveTitleNotes/title,_notes_and_text...` |
| | N4 refusal on notes or text | six `TestDeriveTitleNotes` subtests, the 120 and 64 KiB tests |
| Minor 4 | P1 no `-` to `_`; P2 case sensitive; P3 no trim; P4 no `progress` alias | `TestParseStatus` |
| | A1 operator not "you"; A2 unnamed agent not "agent"; A3 name not used | `TestActorLabel` |
| Minor 5 | U1 no length check after escaping | `TestCleanURL` (the `multibyte` row) |

The mutants you named: IsControl narrowed (C1), U+200C added to `hidden` (J1), a range end moved in (R1 to R4), the title cut removed (T1), the notes-and-text refusal removed (N1).

### For the controller

1. The `ParseStatus` and `Actor.Label` tables are in clean_test.go: the brief names it as this task's test file and the plan's file structure has no `model_test.go`. If you add `model_test.go` to the plan, that is their natural home (a move, not a rewrite).
2. The commit subject says "every cap". The caps pinned now are the ones the review listed: title 200, derived title 120, notes 64 KiB, and the `cutRunes` and `cutBytes` boundaries. Not pinned, outside the review list: the URL cap at exactly 2,048 bytes (accepted) and 2,049 (refused), and the 64-character caps of `nameRE` and `clientIDRE` (the brief tests 65 characters refused for names and nothing over the limit for client ids; nothing tests 64 accepted). Say if you want those rows.
3. Adding U+200B and U+2060 to `hidden` would survive the suite: no ruling protects them (U+200C, U+200D and all of category Cf are protected).
4. Unchanged residuals from Concerns 3: invisible characters outside `hidden` still pass cleaning, and `cutRunes` still cuts between code points, not grapheme clusters.
