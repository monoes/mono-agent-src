package main

import (
	"context"
	"sync"

	"github.com/monoes/mono-agent/internal/capturesummary"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/monomind"
)

// The form-filling half of the extension bridge: form.fill reads the named
// profile's own documents and captures (never the global brain) and asks a
// tool-less model turn which field each fact belongs to
// (internal/extension/form_fill.go). Like the task sink it opens the database
// per request and never creates one.
type formFiller struct {
	path    string
	runtime string
	run     capturesummary.RunFunc
}

func extensionFormFiller(path, runtime string) extension.FormFiller {
	return formFiller{path: path, runtime: runtime, run: capturesummary.ExecRunner(0)}
}

// Facts implements extension.FormFiller.
func (f formFiller) Facts(ctx context.Context, profile string, queries []string) ([]extension.FormFact, error) {
	db, err := openProfileDB(f.path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		facts []extension.FormFact
		fails int
		first error
	)
	for _, q := range queries {
		wg.Add(1)
		go func(q string) {
			defer wg.Done()
			res, err := monomind.SearchKnowledge(ctx, db.DB, profile, q)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fails++
				if first == nil {
					first = err
				}
				return
			}
			for _, r := range res {
				facts = append(facts, extension.FormFact{Source: r.Path, Text: r.Excerpt})
			}
		}(q)
	}
	wg.Wait()
	if fails == len(queries) && first != nil {
		return nil, first
	}
	return facts, nil
}

// Complete implements extension.FormFiller.
func (f formFiller) Complete(ctx context.Context, runtime, model, prompt string) (string, error) {
	if runtime == "" {
		runtime = f.runtime
	}
	a, err := f.run(ctx, capturesummary.Target{Runtime: runtime, Model: model}, prompt)
	return a.Text, err
}
