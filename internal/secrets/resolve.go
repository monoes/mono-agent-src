package secrets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
)

const secretRefPrefix = "@secret:"

// ErrResolveRefused is wrapped by the refusal to resolve a reference under
// a context RefuseResolving marked.
var ErrResolveRefused = errors.New("secret references are not read from the vault here")

// refusedError is that refusal: the reference and why, which already says
// what ErrResolveRefused would.
type refusedError struct{ msg string }

func (e *refusedError) Error() string { return e.msg }
func (e *refusedError) Unwrap() error { return ErrResolveRefused }

// ErrDataReference is wrapped by the refusal of a @secret reference that a
// step's config holds only once its expressions were evaluated.
var ErrDataReference = errors.New("a @secret reference that came from the run's data or an expression is not read from the vault")

type refuseResolvingKey struct{}

// RefuseResolving marks ctx: no @secret reference resolves under it, nor a
// {{secret:}} lookup or anything else that reads the vault through Resolve.
// why says what runs under ctx, for the refusal, which names the reference
// and never a value. A run the user hasn't approved is one (the workflow
// package marks it).
func RefuseResolving(ctx context.Context, why string) context.Context {
	return context.WithValue(ctx, refuseResolvingKey{}, why)
}

// ResolveRefusal is why references aren't resolved under ctx; "" when they
// are.
func ResolveRefusal(ctx context.Context) string {
	why, _ := ctx.Value(refuseResolvingKey{}).(string)
	return why
}

// RefusedReference is the refusal of ref under a context RefuseResolving
// marked, as Resolve gives it, or nil: for a reader of the vault that doesn't
// go through Resolve.
func RefusedReference(ctx context.Context, ref string) error {
	if why := ResolveRefusal(ctx); why != "" {
		return &refusedError{ref + " is not read from the vault: " + why}
	}
	return nil
}

// Resolve turns a `@secret:<name>` reference into a decrypted value stored
// in the vault under profileID. Strings that do not carry the `@secret:`
// prefix are returned unchanged. Returns an error if the reference is
// present but no matching entry is found, and also if the entry holds
// multiple fields with none of them keyed `secret` — see DecryptFields for
// the field-picking rules.
func Resolve(ctx context.Context, db *sql.DB, profileID, ref string) (string, error) {
	if !strings.HasPrefix(ref, secretRefPrefix) {
		return ref, nil
	}
	if err := RefusedReference(ctx, ref); err != nil {
		return ref, err
	}
	name := strings.TrimPrefix(ref, secretRefPrefix)

	entries, err := List(ctx, db, profileID)
	if err != nil {
		return ref, fmt.Errorf("secrets.Resolve: %w", err)
	}
	var id string
	for _, e := range entries {
		if e.Name == name {
			id = e.ID
			break
		}
	}
	if id == "" {
		return ref, fmt.Errorf("secrets.Resolve: entry %q not found", name)
	}

	fields, _, err := DecryptFields(ctx, db, profileID, id)
	if err != nil {
		return ref, fmt.Errorf("secrets.Resolve: %w", err)
	}
	if v, ok := fields["secret"]; ok {
		return v, nil
	}
	if len(fields) == 1 {
		for _, v := range fields {
			return v, nil
		}
	}
	return ref, fmt.Errorf("secrets.Resolve: entry %q holds multiple fields; a bare reference needs exactly one field, or one keyed \"secret\"", name)
}

// ResolveConfig walks a config map and replaces any string value that starts
// with "@secret:" with its decrypted value from the vault. Mirrors
// vault.ResolveConfig's behavior: a missing/not-found secret is non-fatal —
// the original "@secret:" string is left in place and a warning is logged to
// stderr, rather than failing the whole config resolution. This is a
// deliberate difference from Resolve/DecryptEntry's own not-found behavior
// (which errors), because at this integration point matching the
// established @img- convention is what's consistent with the surrounding
// code (see internal/workflow/execution.go, right after its
// vault.ResolveConfig call). A reference ctx refuses (RefuseResolving) is
// the one error it returns: the step must not run with it.
//
// stored is the same config as the workflow holds it, before its expressions
// were evaluated into config. A reference is the workflow's own only where
// stored holds it as it is, in a value with no expression. A value an
// expression produced (the run's input, a webhook body, an earlier step's
// output, or text the expression built) that a resolver would read as a
// reference, one that starts with "@secret:" after any spaces, is refused at
// any depth of config, and the step doesn't run. Other text that holds
// "@secret:" stays text (see DataReference).
func ResolveConfig(ctx context.Context, db *sql.DB, profileID string, stored, config map[string]interface{}) error {
	if db == nil {
		return nil
	}
	if path, ok := dataReference(stored, config, "", false); ok {
		return fmt.Errorf("%q: %w: only one written as it is in the workflow resolves", path, ErrDataReference)
	}
	for k, v := range config {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if !strings.HasPrefix(s, secretRefPrefix) {
			continue
		}
		resolved, err := Resolve(ctx, db, profileID, s)
		if errors.Is(err, ErrResolveRefused) {
			return err
		}
		if err != nil {
			// Non-fatal: leave original ref, emit warning.
			fmt.Fprintf(os.Stderr, "secrets: warning: %v\n", err)
			continue
		}
		config[k] = resolved
	}
	return nil
}

// DataReference reports where in resolved a value an expression produced
// would be read as a @secret reference: a string that starts with "@secret:"
// after any spaces (what ResolveConfig, browser.jev's values and ai.choose's
// key resolve), or, with inText, one that holds it anywhere (a field whose
// node resolves references inside its text, as mcp.call's headers and env
// do: "Bearer @secret:x"). stored is the same value as the workflow holds
// it; a string it holds as written, with no expression, is the workflow's
// own. path is "" when resolved itself is that string.
func DataReference(stored, resolved interface{}, inText bool) (path string, found bool) {
	return dataReference(stored, resolved, "", inText)
}

// dataReference walks maps and lists of any type, since an expression can
// return typed ones (split's []string).
func dataReference(stored, resolved interface{}, path string, inText bool) (string, bool) {
	r := reflect.ValueOf(resolved)
	for r.Kind() == reflect.Interface || r.Kind() == reflect.Pointer {
		if r.IsNil() {
			return "", false
		}
		r = r.Elem()
	}
	switch r.Kind() {
	case reflect.String:
		if s, ok := literal(stored); ok && s == r.String() {
			return "", false
		}
		if inText {
			return path, strings.Contains(r.String(), secretRefPrefix)
		}
		return path, strings.HasPrefix(strings.TrimSpace(r.String()), secretRefPrefix)
	case reflect.Map:
		for _, k := range r.MapKeys() {
			key := fmt.Sprint(k.Interface())
			p := key
			if path != "" {
				p = path + "." + key
			}
			if found, ok := dataReference(storedAt(stored, k), r.MapIndex(k).Interface(), p, inText); ok {
				return found, true
			}
		}
	case reflect.Slice, reflect.Array:
		if r.Type().Elem().Kind() == reflect.Uint8 {
			return "", false // bytes are a file's content, which nothing resolves
		}
		for i := 0; i < r.Len(); i++ {
			if found, ok := dataReference(storedAt(stored, reflect.ValueOf(i)), r.Index(i).Interface(), fmt.Sprintf("%s[%d]", path, i), inText); ok {
				return found, true
			}
		}
	}
	return "", false
}

// literal is stored as a string with no expression in it.
func literal(stored interface{}) (string, bool) {
	v := reflect.ValueOf(stored)
	if v.Kind() != reflect.String || strings.Contains(v.String(), "{{") {
		return "", false
	}
	return v.String(), true
}

// storedAt is stored's element at key (a map key or a list index), nil when
// it has none.
func storedAt(stored interface{}, key reflect.Value) interface{} {
	v := reflect.ValueOf(stored)
	for v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Map:
		if key.Type().ConvertibleTo(v.Type().Key()) {
			if e := v.MapIndex(key.Convert(v.Type().Key())); e.IsValid() {
				return e.Interface()
			}
		}
	case reflect.Slice, reflect.Array:
		if key.Kind() == reflect.Int && int(key.Int()) < v.Len() {
			return v.Index(int(key.Int())).Interface()
		}
	}
	return nil
}
