package extension

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// The bridge's self-description, served unauthenticated at
// /monoagent/health.
//
// Three different situations used to look identical from the browser's
// side — the extension could only report "Disconnected" and leave the user
// to guess which one they were in:
//
//   - nothing is listening at all (no bridge process): the HTTP request
//     fails outright, and FetchStatus says so;
//   - something is listening but it is not us (most often a Chrome started
//     with --remote-debugging-port=9222, which answers on that port and
//     404s this path): Service is absent, and FetchStatus rejects it;
//   - the bridge is up but this extension is not paired with it: Status is
//     StatusUnpaired.
//
// Everything here is already public to any same-machine caller — the port,
// whether a socket is attached, the process id — so no secret is added to
// an endpoint that deliberately has no auth on it. In particular the
// pairing token never appears: an unauthenticated endpoint that hands out
// the credential would make pairing pointless.

// ServiceName marks a response as coming from this bridge and nothing
// else. Callers that probe a port treat its absence as "not the bridge".
const ServiceName = "monoagent-bridge"

// The values of Status.Status. Kept to three because the extension has to
// turn each one into a different sentence for the user, and a state nobody
// can phrase is a state nobody can act on.
const (
	// StatusConnected: an authenticated extension socket is attached right
	// now. Captures and recall work.
	StatusConnected = "connected"
	// StatusWaiting: the bridge is up and nothing is wrong, but no socket
	// is attached. This is the ordinary resting state of an MV3 extension
	// whose service worker has been suspended — it reconnects on the next
	// event.
	StatusWaiting = "waiting"
	// StatusUnpaired: the bridge recently turned a socket away for
	// presenting the wrong token (or none). The user needs to pair, which
	// is a different instruction from "wait a moment".
	StatusUnpaired = "unpaired"
)

// unpairedWindow bounds how long a rejected handshake keeps colouring the
// reported status. A failure from an hour ago says nothing about now, and
// a bridge stuck on "unpaired" forever would send people to fix something
// that is no longer broken.
const unpairedWindow = 2 * time.Minute

// Status is what /monoagent/health answers. `connected` predates the rest
// of this struct and keeps its exact meaning, because RemoteSender.
// IsConnected and every other local process's probe already read it.
type Status struct {
	Service   string `json:"service"`
	Status    string `json:"status"`
	Connected bool   `json:"connected"`
	Addr      string `json:"addr,omitempty"`
	WSURL     string `json:"wsUrl,omitempty"`
	PID       int    `json:"pid,omitempty"`
	UptimeSec int64  `json:"uptimeSec"`
	Version   string `json:"version,omitempty"`
	// InFlight is how many commands are dispatched to the extension and
	// still waiting for it — 0 means the browser is nobody else's right
	// now. Commands relayed in from another process (a workflow run
	// sharing this bridge) are counted the same as this process's own,
	// because they drive the same browser.
	InFlight int `json:"inFlight"`
	// Browsers is how many browser profiles are attached right now. Only
	// the count is public here; which profile each one runs is behind the
	// relay token, at /monoagent/browsers.
	Browsers int `json:"browsers"`
	// Proof answers a request carrying a `nonce`: HMAC-SHA256 of the
	// pairing token over it (see proof.go). Absent otherwise.
	Proof string `json:"proof,omitempty"`
}

// SetVersion records the build string reported in Status. Optional: an
// empty version is simply omitted rather than reported as "unknown".
func (s *Server) SetVersion(v string) {
	s.statusMu.Lock()
	s.version = v
	s.statusMu.Unlock()
}

// noteAuthFailure records that a socket was turned away for bad
// credentials, which is what StatusUnpaired is derived from.
func (s *Server) noteAuthFailure() {
	s.statusMu.Lock()
	s.lastAuthFailure = time.Now()
	s.statusMu.Unlock()
}

// Status describes this bridge for the extension popup and for
// `monoagentcli bridge status`.
func (s *Server) Status() Status {
	s.statusMu.Lock()
	version := s.version
	lastFail := s.lastAuthFailure
	started := s.startedAt
	s.statusMu.Unlock()

	addr, bound := s.Addr()
	st := Status{
		Service:   ServiceName,
		Status:    StatusWaiting,
		Connected: s.IsConnected(),
		PID:       os.Getpid(),
		Version:   version,
		InFlight:  s.inFlightCommands(),
		Browsers:  len(s.connList()),
	}
	if bound {
		st.Addr = addr
		st.WSURL = "ws://" + addr + "/monoagent"
	}
	if !started.IsZero() {
		st.UptimeSec = int64(time.Since(started).Seconds())
	}
	switch {
	case st.Connected:
		st.Status = StatusConnected
	case time.Since(lastFail) < unpairedWindow:
		// Deliberately only when nothing is attached: a stray unpaired
		// socket while the real extension is happily connected is not
		// something to tell the user their setup is broken over.
		st.Status = StatusUnpaired
	}
	return st
}

// statusProbeTimeout is the budget for one health request. Loopback only,
// and every caller is either a person waiting at a prompt or a popup
// painting itself, so a slow answer is a wrong answer.
const statusProbeTimeout = 800 * time.Millisecond

// FetchStatus reads the bridge's status from baseURL (e.g.
// "http://127.0.0.1:9222"). It fails when nothing is listening, and also
// when something is listening that is not this bridge — a Chrome CDP on
// the same port answers HTTP, and reporting that as a live bridge is how
// the CLI ends up waiting forever for an extension that can never arrive.
func FetchStatus(baseURL string) (Status, error) {
	return FetchStatusTimeout(baseURL, statusProbeTimeout)
}

// FetchStatusTimeout is FetchStatus with a caller-chosen budget, for
// callers polled in the background (`monoagentcli summary`).
func FetchStatusTimeout(baseURL string, timeout time.Duration) (Status, error) {
	client := newBridgeClient(timeout)
	resp, err := client.Get(baseURL + "/monoagent/health")
	if err != nil {
		return Status{}, fmt.Errorf("no bridge answering at %s: %w", baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Status{}, fmt.Errorf("%s is held by something that is not a monoagent bridge (HTTP %d)", baseURL, resp.StatusCode)
	}
	var st Status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return Status{}, fmt.Errorf("%s answered /monoagent/health with something that is not a bridge status: %w", baseURL, err)
	}
	if st.Service != ServiceName {
		return Status{}, fmt.Errorf("%s is held by something that is not a monoagent bridge", baseURL)
	}
	return st, nil
}

// inFlightCommands counts the commands this bridge has sent to the
// extension and is still waiting on. pending holds single-shot commands;
// streams holds captures, which span many frames under one id. The two
// maps never hold the same id, so adding them counts each piece of work
// once.
func (s *Server) inFlightCommands() int {
	s.pendMu.Lock()
	defer s.pendMu.Unlock()
	return len(s.pending) + len(s.streams)
}
