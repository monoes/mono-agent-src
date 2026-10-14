package apipkg

import "fmt"

// hookFieldsMarker in a template check's field set allows hook.*: only the
// webhook block's templates may read it.
const hookFieldsMarker = "\x00hook"

// check runs the semantic checks of a package's webhook block.
func (wh *Webhook) check(credKeys map[string]bool) []string {
	var probs []string
	add := func(format string, a ...any) { probs = append(probs, "webhook: "+fmt.Sprintf(format, a...)) }
	fields := map[string]bool{hookFieldsMarker: true}
	for _, f := range wh.Fields {
		if f.Key == "credential" || f.Key == "credential_id" {
			add("field key %q is reserved", f.Key)
		}
		if fields[f.Key] {
			add("field %q is defined twice", f.Key)
		}
		fields[f.Key] = true
		if !allowedFieldTypes[f.Type] {
			add("field %q: unknown type %q", f.Key, f.Type)
		}
		if (f.Type == "select" || f.Type == "multiselect") && len(f.Options) == 0 {
			add("field %q: a %s needs options", f.Key, f.Type)
		}
	}
	if len(wh.Create) == 0 {
		add("create needs at least one request")
	}
	if len(wh.Delete) == 0 {
		add("delete needs at least one request")
	}
	steps := func(name string, ss []Step) {
		for i, st := range ss {
			where := fmt.Sprintf("webhook %s[%d]", name, i)
			if st.Upload {
				add("%s[%d]: upload is not available in a webhook block", name, i)
			}
			probs = append(probs, checkStep(where, st, fields, credKeys)...)
			probs = append(probs, checkTemplates(where+" for_each", st.ForEach, fields, credKeys)...)
		}
	}
	steps("create", wh.Create)
	steps("delete", wh.Delete)
	probs = append(probs, checkTemplates("webhook id", wh.ID, fields, credKeys)...)
	for k, t := range wh.Extra {
		probs = append(probs, checkTemplates("webhook extra."+k, t, fields, credKeys)...)
	}
	if c := wh.Check; c != nil {
		steps("check", []Step{c.Step})
		probs = append(probs, checkTemplates("webhook check.exists_when", c.ExistsWhen, fields, credKeys)...)
	}
	if v := wh.Verify; v != nil {
		if v.Signature == "" {
			add("verify.signature is required (e.g. \"{{ hdr.X-Hub-Signature-256 }}\")")
		}
		if _, ok := webhookAlgs[v.Alg]; !ok && v.Alg != "" {
			add("verify.alg %q must be sha256, sha1, sha512 or token", v.Alg)
		}
		if v.Encoding != "" && v.Encoding != "hex" && v.Encoding != "base64" {
			add("verify.encoding %q must be hex or base64", v.Encoding)
		}
		if e := v.SecretEncoding; e != "" && e != "hex" && e != "base64" {
			add("verify.secret_encoding %q must be hex or base64", e)
		}
		probs = append(probs, checkTemplates("webhook verify", []any{v.Signature, v.Message, v.Secret}, fields, credKeys)...)
	}
	return probs
}
