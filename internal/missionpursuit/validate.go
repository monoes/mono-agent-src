package missionpursuit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/tasks"
)

// A Criterion is one thing the ultimate goal is held to. Kind is command, file, output (machine criteria, read by
// finish.ParsePlan) or rubric (the person decides).
type Criterion struct {
	Kind     string `json:"kind"`
	Spec     string `json:"spec"`
	Expected string `json:"expected,omitempty"`
}

// The bounds of a criteria set.
const (
	MaxCriteria    = 16
	MaxSpecBytes   = 2048
	MaxExpectBytes = 1024
)

// KindRubric is the criterion the person decides.
const KindRubric = "rubric"

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", tasks.ErrInvalid, fmt.Sprintf(format, a...))
}

// Validate checks a criteria set: 1 to 16 criteria of kind command, file, output or rubric, at least one rubric (the
// person is always the final judge), specs up to 2 KiB and expectations up to 1 KiB without control characters, every
// machine criterion one finish.ParsePlan can run, and no criterion twice.
func Validate(cs []Criterion) error {
	if len(cs) == 0 || len(cs) > MaxCriteria {
		return invalid("a mission goal has 1 to %d criteria, not %d", MaxCriteria, len(cs))
	}
	seen := map[string]bool{}
	rubrics := 0
	for i, c := range cs {
		id := fmt.Sprintf("c%d", i+1)
		if err := validateOne(c); err != nil {
			return invalid("criterion %s: %v", id, err)
		}
		if c.Kind == KindRubric {
			rubrics++
		}
		h := criterionHash(c)
		if seen[h] {
			return invalid("criterion %s repeats an earlier one", id)
		}
		seen[h] = true
	}
	if rubrics == 0 {
		return invalid("a mission goal needs at least one rubric criterion: the person is always the final judge")
	}
	return nil
}

func validateOne(c Criterion) error {
	switch c.Kind {
	case "command", "file", "output", KindRubric:
	default:
		return fmt.Errorf("the kind %q is not command, file, output or rubric", c.Kind)
	}
	if strings.TrimSpace(c.Spec) == "" {
		return fmt.Errorf("the spec is empty")
	}
	if len(c.Spec) > MaxSpecBytes || len(c.Expected) > MaxExpectBytes {
		return fmt.Errorf("the spec is at most %d bytes and the expectation at most %d", MaxSpecBytes, MaxExpectBytes)
	}
	prose := c.Kind == KindRubric // a rubric is prose: line breaks and tabs are fine in its spec
	if err := cleanText("spec", c.Spec, prose); err != nil {
		return err
	}
	if err := cleanText("expected", c.Expected, false); err != nil {
		return err
	}
	if c.Kind != KindRubric {
		if _, err := finish.ParsePlan(c.Kind, c.Spec, c.Expected); err != nil {
			return err
		}
	}
	return nil
}

func cleanText(what, s string, allowBreaks bool) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("the %s is not valid UTF-8", what)
	}
	for _, r := range s {
		if (r < 0x20 && !(allowBreaks && (r == '\n' || r == '\t'))) || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return fmt.Errorf("the %s holds a control character", what)
		}
	}
	return nil
}

// criterionHash is the hash of one criterion, under its own domain. The fields are separated by NUL, which no field can hold.
func criterionHash(c Criterion) string {
	return digest("monoes/mission/criterion/v1", c.Kind, c.Spec, c.Expected)
}

// Hash is the hash of a whole ordered criteria set: what the person is shown and what a confirmation is given back.
func Hash(cs []Criterion) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, criterionHash(c))
	}
	return digest("monoes/mission/goal/v1", parts...)
}

func digest(domain string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(domain))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}
