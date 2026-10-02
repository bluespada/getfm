package cli

import (
	"context"
	"fmt"
	"net/http"

	"github.com/bluespada/getfm/pkg/config"
	"github.com/bluespada/getfm/pkg/free"
	"github.com/bluespada/getfm/pkg/provider"
)

// rules is a thin alias so filterFlags can stay free of the package name.
type rules = free.Rules

func parseRules(spec, suffix, allow, deny string) (rules, error) {
	return free.ParseSpec(spec, suffix, allow, deny)
}

// selection is the outcome of applying free rules to every fetched catalog.
type selection struct {
	rows []modelRow
	// models keeps the full Model values, which the probe needs for the
	// provider-specific reference id.
	models map[string][]provider.Model
}

// collect applies the free rules across every provider that was fetched
// successfully, tagging each model with the rule that matched.
func collect(run *catalogRun, r free.Rules) selection {
	sel := selection{models: map[string][]provider.Model{}}
	for _, res := range run.results {
		if res.Err != nil {
			continue
		}
		p, ok := run.file.Get(res.Provider)
		if !ok {
			continue
		}
		matched := r.Filter(res.Models, free.Facts{
			FreeAlways: p.FreeAlways,
			FreeIDs:    p.DeclaredFree(),
		})
		sortModels(matched)
		sel.rows = append(sel.rows, rowsFrom(matched)...)
		sel.models[res.Provider] = matched
	}
	return sel
}

// statuses summarises the fetch for output.
func statuses(run *catalogRun, sel selection) []statusRow {
	out := make([]statusRow, 0, len(run.results))
	for _, res := range run.results {
		row := statusRow{
			Provider:  res.Provider,
			OK:        res.Err == nil,
			Models:    len(res.Models),
			Free:      len(sel.models[res.Provider]),
			ElapsedMS: res.Elapsed,
		}
		if res.Err != nil {
			row.Error = res.Err.Error()
		}
		out = append(out, row)
	}
	return out
}

// fetchClient is the shared entry point every command uses to load catalogs.
func fetchClient(ctx context.Context, client *http.Client, g *globals) (*catalogRun, error) {
	return fetchAll(ctx, client, g)
}

// providerFor resolves a provider by name with a helpful error.
func providerFor(run *catalogRun, name string) (config.Provider, error) {
	p, ok := run.file.Get(name)
	if !ok {
		available := make([]string, 0, len(run.file.Providers))
		for _, c := range run.file.Providers {
			available = append(available, c.Name)
		}
		return config.Provider{}, fmt.Errorf("unknown provider %q: configured providers are %v", name, available)
	}
	return p, nil
}
