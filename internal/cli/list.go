package cli

import (
	"fmt"
	"os"
)

// runList implements "getfm list".
func runList(args []string) int {
	fs := newFlagSet("getfm list")
	var g globals
	g.register(fs)
	var f filterFlags
	f.register(fs)
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

	if *asJSON {
		doc := listDoc{
			Free:      filterDocFrom(f.spec, f.suffix, f.allow),
			Providers: statuses(run, sel),
			Models:    sel.rows,
		}
		if doc.Models == nil {
			doc.Models = []modelRow{}
		}
		if err := writeJSON(doc); err != nil {
			return fail(err)
		}
	} else {
		printModelTable(sel.rows)
		printProviderProblems(run.failures())
		printSummary(run, sel)
	}

	// A run where nothing could be listed is a failure even though it printed
	// a valid, empty result.
	if len(run.results) > 0 && len(run.failures()) == len(run.results) {
		return exitError
	}
	return exitOK
}

// printSummary gives the one-line count a user wants after a listing.
func printSummary(run *catalogRun, sel selection) {
	total, freeCount := 0, 0
	for _, s := range statuses(run, sel) {
		total += s.Models
		freeCount += s.Free
	}
	fmt.Fprintf(os.Stderr, "\n%d free of %d models across %d providers.\n",
		freeCount, total, len(sel.models))
}
