package openaiapi

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/monoes/mono-agent/internal/httpapi"
)

// Handler is what a dedicated /v1 listener serves: GET /health and the /v1
// routes, nothing else. No workflow, node, HIL or org-endpoint route exists
// on it.
func (g *Gateway) Handler(p Policy) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": g.deps.Version})
	})
	g.Mount(mux, p)
	return mux
}

// How long Serve gives running requests to finish, and then how long it waits
// for the turns it had to end to be killed. Variables so a test can shorten
// them.
var (
	shutdownGrace = 10 * time.Second
	drainGrace    = 20 * time.Second
)

// Serve serves Handler(p) on ln until ctx ends, over TLS when tlsCfg is not
// nil, and then shuts down gracefully: requests get shutdownGrace to finish.
// After that the turns still running are ended, and Serve waits (up to
// drainGrace) for their processes to be killed, so that none of them outlives
// the server, before it closes what is left.
//
// There is no server-wide WriteTimeout: a turn or a stream can run for
// minutes, so each response sets its own write deadline.
func (g *Gateway) Serve(ctx context.Context, ln net.Listener, p Policy, tlsCfg *tls.Config) error {
	bound := ln.Addr().String()
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	srv := &http.Server{
		// On a loopback bind the Host guard of the HTTP API applies here too.
		Handler:           httpapi.LoopbackHostGuard(bound, g.Handler(p)),
		ReadHeaderTimeout: 10 * time.Second,
		// ReadTimeout covers the whole request, body included: a chat body of up to
		// Config.BodyLimit has 30 s to arrive, and a slower sender (a large body over a
		// slow link) is cut off with a connection reset, not a JSON error.
		ReadTimeout: 30 * time.Second,
		IdleTimeout: 120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(sctx); err != nil { // a request outlived the grace period
			if !g.Shutdown(drainGrace) {
				g.deps.Logf("turns were still running %s after the server was told to stop", drainGrace)
			}
			_ = srv.Close()
		}
		return nil
	}
}
