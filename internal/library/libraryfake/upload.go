package libraryfake

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// The fake's write side: create (multipart), and the PATCH of listing
// fields.

func readUpload(r *http.Request) ([]byte, map[string]string, error) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return nil, nil, err
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		return nil, nil, fmt.Errorf("file is required")
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, nil, err
	}
	fields := map[string]string{}
	for k, v := range r.MultipartForm.Value {
		if len(v) > 0 {
			fields[k] = v[0]
		}
	}
	return data, fields, nil
}

// MaxUpload is the largest artifact the fake accepts (413 above it).
var MaxUpload = 20 << 20

func (s *Server) create(w http.ResponseWriter, r *http.Request, u *User, scopes []string) {
	if u == nil {
		apiErr(w, 401, "unauthorized", "login required")
		return
	}
	if !contains(scopes, "library:write") {
		apiErr(w, 403, "insufficient_scope", "library:write required")
		return
	}
	data, f, err := readUpload(r)
	if err != nil {
		apiErr(w, 400, "bad_request", err.Error())
		return
	}
	if len(data) > MaxUpload {
		apiErr(w, 413, "too_large", "artifact over 20 MB")
		return
	}
	vis := f["visibility"]
	switch vis {
	case "", "private":
		vis = "private"
	case "public":
	case "official":
		if !u.Admin {
			apiErr(w, 403, "forbidden", "official is admin only")
			return
		}
	default:
		apiErr(w, 400, "bad_request", "bad visibility")
		return
	}
	kind := f["kind"]
	if kind == "workflow" || kind == "org" || kind == "api" {
		var v map[string]any
		if json.Unmarshal(data, &v) != nil {
			apiErr(w, 400, "invalid_artifact", "not JSON")
			return
		}
	} else if kind != "automation" {
		apiErr(w, 400, "bad_request", "bad kind")
		return
	}
	slug := strings.ToLower(strings.ReplaceAll(f["name"], " ", "-"))
	if s.find(kind+"/"+slug) != nil {
		slug = fmt.Sprintf("%s-%d", slug, s.seq+1)
	}
	version := f["version"]
	if version == "" {
		version = "1.0.0"
	}
	id := s.Add(u.Username, kind, slug, f["name"], vis, version, data, nil)
	s.mu.Lock()
	it := s.items[id]
	it.Description = f["description"]
	if t := f["tags"]; t != "" {
		it.Tags = strings.Split(t, ",")
	}
	s.mu.Unlock()
	writeJSON(w, 201, s.itemJSON(it))
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func (s *Server) patch(w http.ResponseWriter, r *http.Request, u *User, id string) {
	it := s.find(id)
	if u == nil {
		apiErr(w, 401, "unauthorized", "login required")
		return
	}
	if it == nil || !visible(it, u) {
		apiErr(w, 404, "not_found", "no such item")
		return
	}
	if it.Owner == nil || it.Owner.ID != u.ID {
		apiErr(w, 403, "forbidden", "not your item")
		return
	}
	var body struct {
		Name, Description, Visibility *string
		Tags                          []string
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiErr(w, 400, "bad_request", err.Error())
		return
	}
	s.mu.Lock()
	if body.Name != nil {
		it.Name = *body.Name
	}
	if body.Description != nil {
		it.Description = *body.Description
	}
	if body.Visibility != nil {
		it.Visibility = *body.Visibility
	}
	if body.Tags != nil {
		it.Tags = body.Tags
	}
	s.mu.Unlock()
	writeJSON(w, 200, s.itemJSON(it))
}
