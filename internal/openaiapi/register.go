package openaiapi

import "net/http"

// Mount registers the /v1 routes on mux for a listener with policy p. Each
// route authenticates with an API key; nothing here reads the legacy HTTP API
// token.
func (g *Gateway) Mount(mux *http.ServeMux, p Policy) {
	mux.Handle("GET /v1/models", g.auth(g.handleModels(p)))
	mux.Handle("GET /v1/models/{id...}", g.auth(g.handleModel(p)))
	mux.Handle("POST /v1/chat/completions", g.auth(g.handleChat(p)))
	mux.Handle("POST /v1/images/generations", g.auth(g.handleImages(p)))
	mux.Handle("GET /v1/images/jobs/{id}", g.auth(g.handleImageJob()))
	mux.Handle("DELETE /v1/images/jobs/{id}", g.auth(g.handleImageJobDelete()))
}
