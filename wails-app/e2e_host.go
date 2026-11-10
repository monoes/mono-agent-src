//go:build e2ehost

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/logger"
)

// THE BROWSER END-TO-END HOST (build tag e2ehost). It exists only for the stub-runtime end-to-end of the open-ended chat mode:
// the real App (every binding, the real CLI behind it) and the real built frontend, served over HTTP to a headless browser, with
// no Wails window. A binding call is POST /__call/<Method> with the arguments as a JSON array; the events the app emits are a
// server-sent stream (/__events). The native confirmation is answered from a file the script writes (answerFile), so the
// scripted "Yes" and "Cancel" are real answers of the real dialog seam (confirmNative), and every dialog text is logged.
// No shipped build carries this file, and it listens only on the loopback address it is given.

const (
	e2eAnswerFile = ".e2e-dialog-answer" // "yes" or "no", read at every dialog
	e2eDialogLog  = ".e2e-dialogs.log"
)

func init() {
	if os.Getenv("MONOAGENT_E2E_ADDR") != "" { // a tagged build without the address is the ordinary app
		runE2EHost()
	}
}

type e2eHub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func (h *e2eHub) publish(name string, data []interface{}) {
	b, err := json.Marshal(map[string]any{"name": name, "data": data})
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.subs {
		select {
		case c <- b:
		default: // a slow page loses an event rather than stalling the app
		}
	}
}

func (h *e2eHub) subscribe() (chan []byte, func()) {
	c := make(chan []byte, 256)
	h.mu.Lock()
	if h.subs == nil {
		h.subs = map[chan []byte]struct{}{}
	}
	h.subs[c] = struct{}{}
	h.mu.Unlock()
	return c, func() { h.mu.Lock(); delete(h.subs, c); h.mu.Unlock() }
}

func e2eConfirm(_ *App, title, body string) (bool, error) {
	if title == pursuitDialogTitle { // a pursuit start is never confirmed over HTTP: a scripted "yes" is not a person's click
		return false, errNoNativeDialog
	}
	home := os.Getenv("HOME")
	if f, err := os.OpenFile(filepath.Join(home, e2eDialogLog), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		fmt.Fprintf(f, "--- %s\n%s\n", title, body)
		f.Close()
	}
	b, _ := os.ReadFile(filepath.Join(home, e2eAnswerFile))
	time.Sleep(dialogMinThink + 300*time.Millisecond) // a decision takes time; the app refuses an answer that is too fast
	return strings.TrimSpace(string(b)) == "yes", nil
}

func runE2EHost() {
	addr := os.Getenv("MONOAGENT_E2E_ADDR")
	dist := os.Getenv("MONOAGENT_E2E_DIST")
	if !strings.HasPrefix(addr, "127.0.0.1:") || dist == "" {
		fmt.Fprintln(os.Stderr, "e2e host: MONOAGENT_E2E_ADDR (127.0.0.1:port) and MONOAGENT_E2E_DIST are required")
		os.Exit(2)
	}
	hub := &e2eHub{}
	eventsEmit = func(_ context.Context, name string, data ...interface{}) { hub.publish(name, data) }
	confirmNative = e2eConfirm
	app := NewApp()
	app.startup(context.WithValue(context.Background(), "logger", logger.NewDefaultLogger()))

	mux := http.NewServeMux()
	mux.HandleFunc("/__shim.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		io.WriteString(w, e2eShim)
	})
	mux.HandleFunc("/__events", func(w http.ResponseWriter, r *http.Request) {
		if !e2eSameOrigin(r, addr) {
			http.Error(w, "refused: not from this page", http.StatusForbidden)
			return
		}
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no streaming", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		c, off := hub.subscribe()
		defer off()
		fmt.Fprint(w, ": ok\n\n")
		fl.Flush()
		for {
			select {
			case b := <-c:
				fmt.Fprintf(w, "data: %s\n\n", b)
				fl.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("/__call/", func(w http.ResponseWriter, r *http.Request) { e2eCall(app, w, r, addr) })
	vault := vaultImageHandler(app)
	mux.HandleFunc("/vault-image/", func(w http.ResponseWriter, r *http.Request) {
		if r.Host != addr { // an image is read by an <img> of the page: no Origin is sent, the Host must be this host's own
			http.Error(w, "refused: not this host", http.StatusForbidden)
			return
		}
		vault.ServeHTTP(w, r)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dist, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			p = filepath.Join(dist, "index.html")
		}
		if filepath.Base(p) == "index.html" {
			b, err := os.ReadFile(p)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(strings.Replace(string(b), "<head>", `<head><script src="/__shim.js"></script>`, 1)))
			return
		}
		http.ServeFile(w, r, p)
	})
	fmt.Fprintln(os.Stderr, "e2e host listening on http://"+addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintln(os.Stderr, "e2e host:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// e2eSameOrigin accepts a call only if its Host is the host's own loopback address and its Origin, when it has one, is that
// address too: another web page in the same browser cannot drive the bindings.
func e2eSameOrigin(r *http.Request, addr string) bool {
	if r.Host != addr {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" && o != "http://"+addr {
		return false
	}
	return true
}

// e2eCall runs one exported method of the App with the JSON arguments, like the Wails bridge does.
func e2eCall(app *App, w http.ResponseWriter, r *http.Request, addr string) {
	if !e2eSameOrigin(r, addr) {
		http.Error(w, "refused: not from this page", http.StatusForbidden)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/__call/")
	m := reflect.ValueOf(app).MethodByName(name)
	if r.Header.Get("Content-Type") != "application/json" { // a form post from another page cannot set this without a preflight
		http.Error(w, "refused: bindings take application/json", http.StatusUnsupportedMediaType)
		return
	}
	if !m.IsValid() || name == "" || r.Method != http.MethodPost {
		http.Error(w, "no such binding: "+name, http.StatusNotFound)
		return
	}
	var raw []json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		http.Error(w, "bad arguments", http.StatusBadRequest)
		return
	}
	mt := m.Type()
	if mt.IsVariadic() || len(raw) != mt.NumIn() {
		http.Error(w, fmt.Sprintf("%s takes %d arguments, got %d", name, mt.NumIn(), len(raw)), http.StatusBadRequest)
		return
	}
	in := make([]reflect.Value, len(raw))
	for i := range raw {
		v := reflect.New(mt.In(i))
		if err := json.Unmarshal(raw[i], v.Interface()); err != nil {
			http.Error(w, "argument "+fmt.Sprint(i)+": "+err.Error(), http.StatusBadRequest)
			return
		}
		in[i] = v.Elem()
	}
	out := m.Call(in)
	var res any
	for _, o := range out {
		if e, ok := o.Interface().(error); ok && e != nil {
			http.Error(w, e.Error(), http.StatusInternalServerError)
			return
		}
		if o.Type() != reflect.TypeOf((*error)(nil)).Elem() {
			res = o.Interface()
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

const e2eShim = `(function () {
  const call = name => (...args) => fetch('/__call/' + name, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(args) }).then(async r => {
    const t = await r.text()
    if (!r.ok) throw new Error(t.trim())
    return t ? JSON.parse(t) : null
  })
  window.go = { main: { App: new Proxy({}, { get: (_, name) => call(String(name)) }) } }
  const handlers = {}
  const es = new EventSource('/__events')
  es.onmessage = m => { const ev = JSON.parse(m.data); (handlers[ev.name] || []).slice().forEach(h => h(...(ev.data || []))) }
  const on = (name, cb) => { (handlers[name] = handlers[name] || []).push(cb); return () => { handlers[name] = (handlers[name] || []).filter(x => x !== cb) } }
  const base = {
    EventsOn: on, EventsOnMultiple: (n, cb) => on(n, cb), EventsOnce: on, EventsOff: n => { delete handlers[n] }, EventsOffAll: () => {},
    EventsEmit: () => {}, Environment: () => Promise.resolve({ platform: 'linux', arch: 'amd64', buildType: 'dev' }),
  }
  window.runtime = new Proxy(base, { get: (t, k) => (k in t ? t[k] : () => Promise.resolve()) })
})();`
