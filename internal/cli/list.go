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
	var out outputFlags
	out.register(fs)
	newOnly := fs.Bool("new-only", false, "list only models the store has not recorded yet")

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	format, err := out.resolve()
	if err != nil {
		return usageErr(err)
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

	storePath, err := resolveStorePath(g.storePath)
	if err != nil {
		return fail(err)
	}
	doc := listDoc{
		Free:      filterDocFrom(f.spec, f.suffix, f.allow),
		Providers: statuses(run, sel),
		Models:    sel.rows,
	}
	newListed := 0
	if *newOnly {
		// Read against the store as it was before this run, and without writing
		// it back: a listing that consumed its own novelty would make the next
		// -new-only call silently empty.
		fresh, err := onlyNew(storePath, run, sel)
		if err != nil {
			return fail(err)
		}
		doc.Models = fresh
		newListed = len(fresh)
	}

	// The table's own empty state talks about the free rules, which are not
	// what decided there was nothing to print. Skipping the table leaves
	// stdout empty either way, since it would have had no rows.
	if format == formatTable && *newOnly && newListed == 0 {
		fmt.Fprintln(os.Stderr, "No model is new since the last run.")
	} else if err := writeListDoc(format, doc); err != nil {
		return fail(err)
	}
	if format != formatJSON {
		printProviderProblems(run.failures())
	}
	switch {
	case *newOnly && !out.quiet:
		printNewSummary(sel, newListed, storePath)
	case !*newOnly && !out.quiet:
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
