package openaiapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
)

// imageContextWhy says why a key created with --context is held to the context
// maximum on this route, where none of its knowledge is added to the turn.
const imageContextWhy = "such a key is held to it on every route, because its chat requests carry excerpts of the profile's knowledge, which includes captured web pages nobody vetted"

// handleImages is POST /v1/images/generations: one turn of a runtime that can make
// images, in a slot folder, told to save what it makes in a folder of the turn's own
// inside it; what it saved there is read before the slot folder is emptied, and returned.
func (g *Gateway) handleImages(p Policy) func(http.ResponseWriter, *http.Request, Principal) {
	return func(w http.ResponseWriter, r *http.Request, pr Principal) {
		begin := time.Now()

		status, model, detail, autoBy := http.StatusOK, "", "", ""
		fail := func(e *apiError) {
			status, detail = e.Status, e.detail
			writeError(w, e)
		}
		defer func() { g.logRequest(pr, begin, model, status, "", autoBy, detail) }()

		var req ImageRequest
		if e := decodeBodyWith(w, r, min(g.cfg.BodyLimit, maxImageBody), &req, imageTypeError); e != nil {
			fail(e)
			return
		}
		n, size, e := validateImages(&req)
		if e != nil {
			fail(e)
			return
		}

		eff := policyFor(p, pr)
		isAuto := req.Model == autoModelID
		if isAuto {
			model = autoModelID
		}
		m, candidates, e := g.resolveImageModel(r.Context(), pr, p, eff, req.Model)
		if m.ID != "" { // the model a refusal is for is in the log line, as for chat
			model = m.ID
		}
		if e != nil {
			fail(e)
			return
		}

		slot, release, ok := g.limiter.tryAcquire()
		if !ok {
			fail(errBusy())
			return
		}
		defer func() { release() }() // a closure: an image job takes the slot over by replacing release

		// The slot comes first, as for chat: a request that would be refused for lack of
		// one never costs a Jev call.
		if isAuto {
			pick, gone := g.pickForRequest(w, r, pr, req.Prompt, candidates)
			if gone { // the caller left while Jev was asked: there is nobody to answer
				status = 499
				return
			}
			m, autoBy, model = pick.Model, pick.By, pick.Model.ID
		}

		w.Header().Set("X-Monoagent-Model", m.ID)
		if wantsAsync(r) {
			// The turn outlives this request: the answer is a job to ask for, so no
			// connection has to stay open while the runtime works.
			job, ok := g.jobs.add(pr.KeyID)
			if !ok {
				fail(errBusy())
				return
			}
			status, detail = http.StatusAccepted, "job accepted "+job.id
			jctx, jcancel := context.WithCancel(g.shutdownCtx)
			g.jobs.attach(job, jcancel)
			go g.runImageJob(jctx, jcancel, job, pr, begin, model, autoBy, eff, m, slot, release, n, size, req.Prompt)
			release = func() {} // the job gives the slot back, not this handler
			writeJobAccepted(w, job)
			return
		}
		extendWriteDeadline(w, g.cfg.TurnTimeout+2*turnGrace)

		res, imgs, detail2, e, gone := g.makeImages(r.Context(), m, eff, pr, slot, release, n, size, req.Prompt)
		if gone {
			status = 499 // the caller left; there is nobody to answer
			return
		}
		if e != nil {
			fail(e)
			return
		}
		detail = detail2
		if res.SandboxStatus != "" {
			w.Header().Set("X-Monoagent-Sandbox", res.SandboxStatus)
		}
		writeImages(w, time.Now().Unix(), imgs, usageFrom(res))
	}
}

// makeImages runs the turn of an image request in the slot, gives the slot back when the
// turn is over, and says what it made: the images, or the error to answer with, or gone
// when the caller left. detail is what the log gets about a success that left files out.
func (g *Gateway) makeImages(ctx context.Context, m ModelInfo, eff Policy, pr Principal, slot int, release func(), n int, size, prompt string) (res *monomind.TurnResult, images [][]byte, detail string, e *apiError, gone bool) {
	var found collected
	folder := newImageFolder()
	t := turn{
		Runtime: m.Runtime, Model: m.Model, System: imageSystemPrompt(folder), Prompt: imagePrompt(prompt, n, size),
		Policy: eff, ProfileID: pr.ProfileID, Slot: slot, RequireSandbox: m.Class == Sandboxed,
		Subdir:  folder,
		Collect: func(ctx context.Context, dir string) { found = collectImages(ctx, dir, folder, n) },
	}
	res, err := g.runTurn(ctx, t)
	release() // the turn is over and its folder is empty: writing the answer is not what the slot is for
	e, gone = g.resultError(ctx, res, err, m, eff)
	if gone || e != nil {
		return res, nil, "", e, gone
	}
	switch {
	case len(found.images) > 0: // what the runtime saved decides: it did not lack the tool
	case saidNoImageTool(res.ResultText):
		return res, nil, "", errNoImageTool(m), false
	default:
		e := errNoImageMade(res.ResultText)
		e.detail = "no image was collected from the turn's folder"
		if note := found.note(); note != "" {
			e.detail += " (left out: " + note + ")"
		}
		return res, nil, "", e, false
	}
	if note := found.note(); note != "" { // a success that left files out says so, too
		detail = "left out: " + note
	}
	return res, found.images, detail, nil, false
}

// resolveImageModel finds the model of an image request: for auto the models it
// may pick among (the model is chosen later), without a model the first installed
// runtime of the image list that the policy allows, otherwise the model named.
func (g *Gateway) resolveImageModel(ctx context.Context, pr Principal, p, eff Policy, name string) (ModelInfo, []ModelInfo, *apiError) {
	if g.cfg.ImagesOff() && (name == "" || name == autoModelID) {
		return ModelInfo{}, nil, errImagesOff() // before anything else is asked: auto's Jev included
	}
	switch name {
	case autoModelID:
		candidates, e := g.autoCandidatesFor(ctx, pr, eff, capImage)
		return ModelInfo{}, candidates, e
	case "":
		m, e := g.defaultImageModel(ctx, pr, p, eff)
		return m, nil, e
	}
	m, err := g.catalog.Resolve(ctx, name)
	switch {
	case errors.Is(err, ErrUnknownModel):
		return ModelInfo{}, nil, errModelNotFound(name)
	case err != nil:
		return ModelInfo{}, nil, catalogError(err)
	case !g.cfg.CanMakeImages(m):
		return m, nil, g.errNotAnImageModel(ctx, eff, m)
	}
	return m, nil, policyRefusal(m, p, eff, imageContextWhy)
}

// defaultImageModel is the default model of the first installed runtime of the
// image list that can make images and that the policy allows.
func (g *Gateway) defaultImageModel(ctx context.Context, pr Principal, p, eff Policy) (ModelInfo, *apiError) {
	var denied Class // the weakest class among the image models the policy refuses; 0 when none was refused
	var why []string // what keeps each runtime of the list that is out from making images
	for _, runtime := range g.cfg.ImageRuntimeList() {
		m, err := g.catalog.Resolve(ctx, runtime) // a bare runtime is its default model
		switch {
		case errors.Is(err, ErrUnknownModel):
			why = append(why, runtime+" is not installed")
			continue
		case err != nil:
			return ModelInfo{}, catalogError(err)
		case !g.cfg.CanMakeImages(m):
			why = append(why, runtime+" runs as chat-only, which has no tool to save a file")
			continue
		case !eff.Allows(m.Class):
			if denied == 0 || m.Class < denied {
				denied = m.Class
			}
			continue
		}
		return m, nil
	}
	if denied != 0 {
		return ModelInfo{}, errNoImagePolicy(pr, p, eff, denied)
	}
	return ModelInfo{}, errNoImageRuntime(why)
}

// logImageRuntimes says, with the first list of models, which runtimes of the image
// list cannot make images here: one that is not installed (a typo, say) and one that is
// installed but chat-only. The list is loaded by the first request that needs one (the
// gateway is built, and the catalog filled, on first use), so that is when the operator
// hears it: from the server's log, and not from a client's 404.
func (g *Gateway) logImageRuntimes(models []ModelInfo) {
	var problems []string
	for _, runtime := range g.cfg.ImageRuntimeList() {
		installed, capable := false, false
		for _, m := range models {
			if m.Runtime == runtime {
				installed, capable = true, capable || g.cfg.CanMakeImages(m)
			}
		}
		switch {
		case !installed:
			problems = append(problems, runtime+" is not installed")
		case !capable:
			problems = append(problems, runtime+" runs as chat-only, which has no tool to save a file")
		}
	}
	if len(problems) > 0 {
		g.deps.Logf("image runtimes (MONOAGENT_API_IMAGE_RUNTIMES) that make no images here: %s", strings.Join(problems, "; "))
	}
}

// imageRuntimes are the runtimes with a model that can make images and that a key with
// the policy eff may use, in the order of the model list: the ones GET /v1/models marks
// for that key.
func (g *Gateway) imageRuntimes(ctx context.Context, eff Policy) []string {
	models, err := g.catalog.Visible(ctx, eff)
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range models {
		if g.cfg.CanMakeImages(m) && !slices.Contains(out, m.Runtime) {
			out = append(out, m.Runtime)
		}
	}
	return out
}

// errNotAnImageModel is the 400 for a model that exists but cannot make images: a bad
// value for model, not a 404 (it exists) and not a 403 (no policy would make it work).
// It points to the models the key may use that can, or says there are none.
func (g *Gateway) errNotAnImageModel(ctx context.Context, eff Policy, m ModelInfo) *apiError {
	var why string
	switch {
	case g.cfg.ImagesOff():
		return errInvalid("invalid_value", "model", fmt.Sprintf("The model %s cannot generate images: image generation is %s.", strconv.Quote(m.ID), imagesOffBy))
	case slices.Contains(g.cfg.ImageRuntimeList(), m.Runtime):
		why = "it runs as chat-only, which has no tool to save a file"
	default:
		why = "its runtime is not one of the image runtimes"
	}
	msg := fmt.Sprintf("The model %s cannot generate images: %s.", strconv.Quote(m.ID), why)
	if runtimes := g.imageRuntimes(ctx, eff); len(runtimes) > 0 {
		msg += " Models of " + strings.Join(runtimes, ", ") + " can; GET /v1/models marks the models that can with the capability image."
	} else {
		msg += " None of the models this key may use can generate images."
	}
	return errInvalid("invalid_value", "model", msg)
}

// errNoImagePolicy is the 403 for a request that names no model when the key's policy
// allows none of the installed image runtimes, the weakest of which runs as need: what
// the operator has to raise is said, --confinement and, for a key created with
// --context, --context-confinement, to the class that would help.
func errNoImagePolicy(pr Principal, p, eff Policy, need Class) *apiError {
	level := "any"
	if need == Sandboxed {
		level = "sandboxed (or any)"
	}
	var raise []string
	if !p.Allows(need) {
		raise = append(raise, "--confinement "+level)
	}
	if pr.Context && !contextCapAllows(p, need) {
		raise = append(raise, "--context-confinement "+level+", because this key was created with --context")
	}
	return errPolicy(fmt.Sprintf("image generation needs a runtime that can write a file, and this key's policy (%s) allows none of the installed image runtimes, the weakest of which runs as %s; the operator can raise it with %s",
		eff, need, strings.Join(raise, ", and ")))
}

// contextCapAllows reports whether the cap on a key created with --context allows a
// class on its own, whatever the listener serves: it is what --context-confinement
// sets, so that the operator is told to raise it only when it is in the way.
func contextCapAllows(p Policy, c Class) bool {
	p.Max = Unconfined
	return p.ForContextKey().Allows(c)
}

// imagesOffBy says that image generation was switched off, and by what: the list can be set to
// none in the environment or in the saved settings (`monoagentcli api config`).
const imagesOffBy = "switched off on this server (the operator set MONOAGENT_API_IMAGE_RUNTIMES to none, in the environment or with `monoagentcli api config`)"

// errImagesOff is the 404 for a request that names no model, or auto, when image
// generation is switched off.
func errImagesOff() *apiError {
	return &apiError{Status: http.StatusNotFound, Type: "invalid_request_error", Code: "model_not_found", Param: "model",
		Message: "Image generation is " + imagesOffBy + "."}
}

// errNoImageRuntime is the 404 for a request that names no model when none of the
// runtimes of the image list can make images: why says what keeps each one out, an
// installed chat-only runtime apart from one that is not installed.
func errNoImageRuntime(why []string) *apiError {
	return &apiError{Status: http.StatusNotFound, Type: "invalid_request_error", Code: "model_not_found", Param: "model",
		Message: "No model on this server can generate images: " + strings.Join(why, "; ") + ". List the available ids with GET /v1/models."}
}

// errNoImageTool is the 400 for a runtime that said it has no image tool.
func errNoImageTool(m ModelInfo) *apiError {
	return errInvalid("image_generation_unsupported", "model", "The model "+strconv.Quote(m.ID)+" has no built-in image generation tool, so it cannot make images. "+
		"GET /v1/models marks the models that can with the capability image.")
}

// errNoImageMade is the 502 for a turn that ended without an image to return, with what
// the runtime said, on one line and cut short.
func errNoImageMade(reply string) *apiError {
	msg := "The runtime did not produce an image"
	if said := runtimeWords(reply, ""); said != "" {
		msg += ". What it said: " + said
	} else {
		msg += " and said nothing"
	}
	return &apiError{Status: http.StatusBadGateway, Type: "api_error", Code: "image_generation_failed", Message: msg}
}
