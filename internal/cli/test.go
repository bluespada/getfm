package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/bluespada/getfm/pkg/probe"
)

// probeRow is the JSON shape of one probe result.
type probeRow struct {
	Provider string  `json:"provider"`
	Model    string  `json:"model"`
	OK       bool    `json:"ok"`
	Status   int     `json:"status"`
	Latency  float64 `json:"latency_ms"`
	Tokens   int64   `json:"tokens,omitempty"`
	Detail   string  `json:"detail,omitempty"`
}

type probeDoc struct {
	Total   int        `json:"total"`
	OK      int        `json:"ok"`
	Failed  int        `json:"failed"`
	Results []probeRow `json:"results"`
}

// runTest implements "getfm test".
func runTest(args []string) int {
	fs := newFlagSet("getfm test")
	var g globals
	g.register(fs)
	var f filterFlags
	f.register(fs)
	all := fs.Bool("all", false, "probe every free model instead of a single one")
	limit := fs.Int("n", 0, "with -all, probe at most this many models")
	concurrency := fs.Int("c", 2, "how many probes to run at once")
	prompt := fs.String("prompt", probe.DefaultPrompt, "prompt to send")
	asJSON := fs.Bool("json", false, "emit JSON instead of a table")

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	r, err := f.rules()
	if err != nil {
		return usageErr(err)
	}

	ctx, cancel := commandContext()
	defer cancel()

	run, err := fetchClient(ctx, newClient(g.timeout), &g)
	if err != nil {
		return fail(err)
	}
	sel := collect(run, r)

	targets, err := resolveTargets(run, sel, *all, *limit, fs.Args())
	if err != nil {
		return usageErr(err)
	}
	if len(targets) == 0 {
		fmt.Fprintln(os.Stderr, "getfm: nothing to probe")
		return exitUsage
	}
	printProviderProblems(run.failures())

	requests := make([]probe.Request, 0, len(targets))
	warned := map[string]bool{}
	for _, t := range targets {
		p, err := providerFor(run, t.Provider)
		if err != nil {
			return usageErr(err)
		}
		if p.Completions == nil {
			fmt.Fprintf(os.Stderr, "getfm: %s declares no completions endpoint, skipping\n", p.Name)
			continue
		}
		key := keyFor(p, &g)
		// Worth saying out loud once: an unauthenticated probe fails for a
		// reason that has nothing to do with the endpoint itself.
		if key == "" && len(p.EnvKeys) > 0 && !warned[p.Name] {
			warned[p.Name] = true
			fmt.Fprintf(os.Stderr, "getfm: no API key for %s (set %s or pass -key %s=...)\n",
				p.Name, strings.Join(p.EnvKeys, " or "), p.Name)
		}
		requests = append(requests, probe.Request{
			Provider: p,
			Model:    t.Model,
			Ref:      t.Ref,
			Prompt:   *prompt,
			Key:      key,
		})
	}
	if len(requests) == 0 {
		fmt.Fprintln(os.Stderr, "getfm: nothing to probe")
		return exitUsage
	}

	results := probe.SendAll(ctx, newClient(g.timeout), requests, *concurrency)

	rows := make([]probeRow, 0, len(results))
	okCount := 0
	for _, res := range results {
		row := probeRow{
			Provider: res.Provider,
			Model:    res.Model,
			OK:       res.OK,
			Status:   res.Status,
			Latency:  float64(res.Latency.Microseconds()) / 1000,
			Tokens:   res.Tokens,
			Detail:   res.Detail,
		}
		rows = append(rows, row)
		if res.OK {
			okCount++
		}
	}

	if *asJSON {
		doc := probeDoc{Total: len(rows), OK: okCount, Failed: len(rows) - okCount, Results: rows}
		if err := writeJSON(doc); err != nil {
			return fail(err)
		}
	} else {
		printProbeTable(rows)
		fmt.Fprintf(os.Stderr, "\n%d of %d endpoints responded successfully.\n", okCount, len(rows))
	}

	if okCount != len(rows) {
		return exitError
	}
	return exitOK
}

// target is one model to probe.
type target struct {
	Provider string
	Model    string
	Ref      string
}

// resolveTargets works out what the user asked to probe, from either an
// explicit model reference or -all.
func resolveTargets(run *catalogRun, sel selection, all bool, limit int, args []string) ([]target, error) {
	if all {
		var out []target
		for _, res := range run.results {
			if res.Err != nil {
				continue
			}
			for _, m := range sel.models[res.Provider] {
				out = append(out, target{Provider: m.Provider, Model: m.ID, Ref: m.Ref})
				if limit > 0 && len(out) >= limit {
					return out, nil
				}
			}
		}
		return out, nil
	}

	if len(args) != 1 {
		return nil, fmt.Errorf("give exactly one model as provider/model, or use -all")
	}
	arg := args[0]

	if name, model, ok := splitModelRef(arg); ok {
		return []target{{Provider: name, Model: model, Ref: findRef(run, name, model)}}, nil
	}

	// A bare model id is probed against every configured provider that lists
	// it, so "getfm test gpt-4o-mini" does the obvious thing.
	var out []target
	for _, res := range run.results {
		for _, m := range res.Models {
			if m.ID == arg {
				out = append(out, target{Provider: res.Provider, Model: m.ID, Ref: m.Ref})
			}
		}
	}
	if len(out) == 0 {
		names := make([]string, 0, len(run.file.Providers))
		for _, p := range run.file.Providers {
			names = append(names, p.Name)
		}
		return nil, fmt.Errorf("no configured provider lists %q: write it as provider/model (%v)",
			arg, names)
	}
	return out, nil
}

// findRef returns the identifier to send for a model, preferring what the
// catalog reported and falling back to the configured prefix strip for models
// that were named directly rather than discovered.
func findRef(run *catalogRun, providerName, model string) string {
	for _, res := range run.results {
		if res.Provider != providerName {
			continue
		}
		for _, m := range res.Models {
			if m.ID == model {
				return m.Ref
			}
		}
	}
	if p, ok := run.file.Get(providerName); ok {
		return strings.TrimPrefix(model, p.Mapping.StripPrefix)
	}
	return model
}

func printProbeTable(rows []probeRow) {
	t := newTable()
	fmt.Fprintln(t, "PROVIDER\tMODEL\tRESULT\tLATENCY\tTOKENS\tDETAIL")
	for _, r := range rows {
		result := fmt.Sprintf("%d", r.Status)
		if r.Status == 0 {
			result = "ERR"
		}
		detail := r.Detail
		if detail == "" && r.OK {
			detail = "-"
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\t%s\n",
			r.Provider, r.Model, result,
			fmt.Sprintf("%.0fms", r.Latency),
			formatTokens(r.Tokens),
			detail)
	}
	t.Flush()
}

func formatTokens(n int64) string {
	if n <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", n)
}
