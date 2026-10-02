package cli

import (
	"fmt"
)

// providerDoc is the JSON shape of the providers report.
type providerDoc struct {
	Providers []providerEntry `json:"providers"`
}

type providerEntry struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Endpoint    string `json:"models_endpoint"`
	Completions string `json:"completions_endpoint,omitempty"`
	KeySource   string `json:"key_source,omitempty"`
	HasKey      bool   `json:"has_key"`
	FreeAlways  bool   `json:"free_always"`
	Models      int    `json:"models,omitempty"`
	Error       string `json:"error,omitempty"`
}

// runProviders implements "getfm providers": it shows what is configured and
// whether each provider answered. It never prints key material, only where the
// key came from.
func runProviders(args []string) int {
	fs := newFlagSet("getfm providers")
	var g globals
	g.register(fs)
	asJSON := fs.Bool("json", false, "emit JSON instead of a table")

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	ctx, cancel := commandContext()
	defer cancel()

	run, err := fetchClient(ctx, newClient(g.timeout), &g)
	if err != nil {
		return fail(err)
	}

	doc := providerDoc{}
	for i := range run.file.Providers {
		p := run.file.Providers[i]
		source := p.KeySource(g.keyFlags, envLookup)
		entry := providerEntry{
			Name:       p.Name,
			Label:      p.Label,
			Endpoint:   p.ModelsURL(),
			KeySource:  source,
			HasKey:     source != "",
			FreeAlways: p.FreeAlways,
		}
		if p.Completions != nil {
			entry.Completions = p.Completions.Path
		}
		for _, res := range run.results {
			if res.Provider == p.Name {
				entry.Models = len(res.Models)
				if res.Err != nil {
					entry.Error = res.Err.Error()
				}
			}
		}
		doc.Providers = append(doc.Providers, entry)
	}

	if *asJSON {
		if err := writeJSON(doc); err != nil {
			return fail(err)
		}
		return exitOK
	}

	t := newTable()
	fmt.Fprintln(t, "PROVIDER\tMODELS\tKEY\tPROBE\tFREE\tENDPOINT")
	for _, e := range doc.Providers {
		models := fmt.Sprintf("%d", e.Models)
		if e.Error != "" {
			models = "err"
		}
		key := e.KeySource
		if key == "" {
			key = "-"
		}
		probeable := "no"
		if e.Completions != "" {
			probeable = "yes"
		}
		freeAlways := "-"
		if e.FreeAlways {
			freeAlways = "always"
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\t%s\n", e.Name, models, key, probeable, freeAlways, e.Endpoint)
	}
	t.Flush()

	printProviderProblems(run.failures())
	return exitOK
}
