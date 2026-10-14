package apipkg

import "net/http"

// checkErrorWhen fails a 2xx response that reports an error in its body
// (Odoo JSON-RPC answers HTTP 200 with {"error": {…}}): the op's
// error_when, else the package's, renders over res (the decoded body),
// hdr and cred; when it is true the item fails with error_message.
func (r *opRun) checkErrorWhen(body []byte, header http.Header) error {
	when, msg := r.op.ErrorWhen, r.op.ErrorMessage
	if when == "" {
		when = r.pkg.ErrorWhen
	}
	if msg == "" {
		msg = r.pkg.ErrorMessage
	}
	if when == "" {
		return nil
	}
	sc := r.scope
	sc.Res = decodeBody(body)
	sc.Hdr = headerMap{}
	for k, vs := range header {
		if len(vs) > 0 {
			sc.Hdr[k] = vs[0]
		}
	}
	w, err := renderString(when, sc)
	if err != nil {
		return r.errf("error_when: %w", err)
	}
	if !truthy(w) {
		return nil
	}
	text := ""
	if msg != "" {
		if text, err = renderText(msg, sc); err != nil {
			return r.errf("error_message: %w", err)
		}
	}
	if text == "" {
		text = errorBodyEcho(body)
	}
	return r.errf("the API reported an error: %s", text)
}
