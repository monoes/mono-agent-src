package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/taskview"
)

// The documents the task tools return are the task views' (internal/taskview):
// the HTTP API's task routes return the same ones.
const (
	untrustedNote    = taskview.UntrustedNote
	listNotesRunes   = taskview.ListNotesRunes
	taskGetEventsMax = taskview.EventsMax
)

type (
	taskView       = taskview.View
	eventView      = taskview.EventView
	taskListResult = taskview.ListResult
	taskResult     = taskview.Result
	taskGetResult  = taskview.GetResult
)

func viewOf(t tasks.Task) taskView                       { return taskview.Of(t) }
func viewPtr(t *tasks.Task) *taskView                    { return taskview.Ptr(t) }
func listView(t tasks.Task) taskView                     { return taskview.ListView(t) }
func listViews(ts []tasks.Task) []taskView               { return taskview.ListViews(ts) }
func cutListNotes(s string) string                       { return taskview.CutListNotes(s) }
func recentEvents(es []tasks.Event) ([]tasks.Event, int) { return taskview.RecentEvents(es) }
func eventViews(es []tasks.Event) []eventView            { return taskview.EventViews(es) }

// taskToolErr is a store error as a task tool returns it: its code first (spec
// 5.1), so a model can act on it, then the store's words, which hold ids,
// statuses, names and times and never a task's text. An invalid-input or limit
// error loses its sentinel's words, which only repeat the code. Other errors
// pass as they are.
func taskToolErr(err error) error {
	var claimed *tasks.ClaimedError
	code, repeats := "", error(nil)
	switch {
	case err == nil:
		return nil
	case errors.As(err, &claimed):
		code = "claimed"
	case errors.Is(err, tasks.ErrNotFound):
		code = "not_found"
	case errors.Is(err, tasks.ErrOperatorOnly):
		code = "operator_only"
	case errors.Is(err, tasks.ErrNotReady):
		code = "not_ready"
	case errors.Is(err, tasks.ErrNotClaimant):
		code = "not_claimant"
	case errors.Is(err, tasks.ErrLimit):
		code, repeats = "limit", tasks.ErrLimit
	case errors.Is(err, tasks.ErrInvalid):
		code, repeats = "invalid_input", tasks.ErrInvalid
	default:
		return err
	}
	msg := err.Error()
	if repeats != nil {
		msg = strings.TrimPrefix(msg, repeats.Error()+": ")
	}
	return codedErr{text: code + ": " + msg, err: err}
}

// codedErr is a refusal's text with the store's error still inside, for errors.Is.
type codedErr struct {
	text string
	err  error
}

func (e codedErr) Error() string { return e.text }
func (e codedErr) Unwrap() error { return e.err }

// invalidArgs is the refusal of a task tool's arguments.
func invalidArgs(format string, a ...any) error {
	return fmt.Errorf("invalid_input: %s", fmt.Sprintf(format, a...))
}

// decodeTaskArgs reads a task tool's arguments into dst, a pointer to a struct
// whose json tags are the arguments the tool takes (a field with no name there,
// or "-", takes none), and refuses any other: a model that sends a profile, a
// name to claim under or a column to add into would otherwise believe it chose
// them. The server serves one profile and names its caller, and an agent's task
// always lands in Inbox. A refusal is said in a model's words, never Go's: it
// names every argument the tool does not take and lists those it does, or names
// the argument whose value it cannot read. A dst that is no pointer to a struct
// is the caller's mistake, returned as one and not as invalid input.
func decodeTaskArgs(args json.RawMessage, dst any) error {
	target := reflect.ValueOf(dst)
	if target.Kind() != reflect.Pointer || target.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("decodeTaskArgs: dst is %T, want a pointer to a struct", dst)
	}
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var given map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &given); err != nil {
		return invalidArgs("the arguments are not a JSON object")
	}
	fields := target.Elem()
	takes := map[string]int{} // each argument the tool takes, and the field that holds it
	for i := 0; i < fields.NumField(); i++ {
		field := fields.Type().Field(i)
		if !field.IsExported() {
			continue // it cannot be set, and so is no argument
		}
		if name := strings.Split(field.Tag.Get("json"), ",")[0]; name != "" && name != "-" {
			takes[name] = i
		}
	}
	names := slices.Sorted(maps.Keys(given))
	var unknown []string
	for _, name := range names {
		if _, ok := takes[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		return refuseUnknown(unknown, takes)
	}
	for _, name := range names {
		if err := json.Unmarshal(given[name], fields.Field(takes[name]).Addr().Interface()); err != nil {
			return refuseValue(name, err)
		}
	}
	return nil
}

// refuseUnknown is the refusal of arguments a tool does not take: it names every
// one, and lists those the tool does take, so that a model learns what to send.
func refuseUnknown(unknown []string, takes map[string]int) error {
	quoted := make([]string, len(unknown))
	for i, name := range unknown {
		quoted[i] = strconv.Quote(name)
	}
	subject := quoted[0] + " is not an argument"
	if len(quoted) > 1 {
		subject = strings.Join(quoted, ", ") + " are not arguments"
	}
	list := "it takes no arguments"
	if names := slices.Sorted(maps.Keys(takes)); len(names) > 0 {
		list = "it takes only " + strings.Join(names, ", ")
	}
	return invalidArgs("%s of this tool: %s (this server serves one profile and names its caller itself)", subject, list)
}

// refuseValue is the refusal of one argument's value. The store's own refusal
// reads as it does everywhere else; a value of the wrong JSON type is said in
// words about the type, not Go's; any other is an argument type's own words,
// after the argument's name.
func refuseValue(name string, err error) error {
	var wrongType *json.UnmarshalTypeError
	switch {
	case errors.Is(err, tasks.ErrInvalid):
		return taskToolErr(err)
	case errors.As(err, &wrongType):
		return invalidArgs("%s: expected %s", name, kindWords(wrongType.Type))
	}
	return invalidArgs("%s: %v", name, err)
}

// kindWords is what a model calls a value of Go type t.
func kindWords(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "a whole number"
	}
	return "another kind of value"
}

// floatForm is a number as JSON writes one (digits, a fraction, an exponent),
// each part bounded, so that a hostile text costs nothing to read.
var floatForm = regexp.MustCompile(`^[+-]?[0-9]{1,20}(\.[0-9]{1,20})?([eE][+-]?[0-9]{1,3})?$`)

// wholeNumber reads text as a whole number: 12, or what a client that keeps its
// numbers as floats writes for it, 12.0 or 1.2e1. It is exact, with no float in
// it, so 9007199254740993.0 is 9007199254740993; 12.5 is not a whole number, nor
// is one that does not fit an int64.
func wholeNumber(text string) (int64, bool) {
	if !floatForm.MatchString(text) {
		return 0, false
	}
	r, ok := new(big.Rat).SetString(text)
	if !ok || !r.IsInt() || !r.Num().IsInt64() {
		return 0, false
	}
	return r.Num().Int64(), true
}

// outOfRange says whether text is a whole number too large (or small) for an int64, so that
// the refusal can say the range rather than ask again for a whole number.
func outOfRange(text string) bool {
	if !floatForm.MatchString(text) {
		return false
	}
	r, ok := new(big.Rat).SetString(text)
	return ok && r.IsInt() && !r.Num().IsInt64()
}

// taskIDArg is a task's number: 12, or "12" or "#12" as a model may write it, or
// a float that holds it (12.0).
type taskIDArg int64

func (id *taskIDArg) UnmarshalJSON(b []byte) error {
	raw := strings.TrimSpace(string(b))
	if raw == "null" {
		return nil
	}
	if unquoted, err := strconv.Unquote(raw); err == nil {
		raw = strings.TrimPrefix(strings.TrimSpace(unquoted), "#")
	}
	n, ok := wholeNumber(raw)
	if !ok && outOfRange(raw) {
		return errors.New("expected a task's number, such as 12, that fits the supported range")
	}
	if !ok || n <= 0 {
		return errors.New("expected a task's number, such as 12")
	}
	*id = taskIDArg(n)
	return nil
}

// need is the id, or the refusal of a call that gave none.
func (id taskIDArg) need() (int64, error) {
	if id <= 0 {
		return 0, invalidArgs("id is required: the task's number, as task_list shows it")
	}
	return int64(id), nil
}

// numberArg is a whole number, or a numeric string as a model may write it
// ("20"), or a float that holds one (20.0): task_list's limit and task_claim's
// lease_minutes.
type numberArg int64

func (n *numberArg) UnmarshalJSON(b []byte) error {
	raw := strings.TrimSpace(string(b))
	if raw == "null" {
		return nil
	}
	if unquoted, err := strconv.Unquote(raw); err == nil {
		raw = strings.TrimSpace(unquoted)
	}
	v, ok := wholeNumber(raw)
	if !ok && outOfRange(raw) {
		return errors.New("expected a whole number, such as 20, that fits the supported range")
	}
	if !ok {
		return errors.New("expected a whole number, such as 20")
	}
	*n = numberArg(v)
	return nil
}

// statusArg is task_list's status: one column, several separated by commas, or
// a list of them.
type statusArg []tasks.Status

func (s *statusArg) UnmarshalJSON(b []byte) error {
	var one string
	var names []string
	switch {
	case json.Unmarshal(b, &one) == nil:
		names = strings.Split(one, ",")
	case json.Unmarshal(b, &names) == nil:
	default:
		return errors.New("expected a column name or a list of them")
	}
	out := statusArg{}
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			continue
		}
		st, err := tasks.ParseStatus(name)
		if err != nil {
			return err
		}
		out = append(out, st)
	}
	*s = out
	return nil
}
