package cli

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bluespada/getfm/pkg/provider"
	"github.com/bluespada/getfm/pkg/store"
)

// diffDoc is the JSON document emitted by diff.
type diffDoc struct {
	// Store is the file the comparison was made against and written to.
	Store string `json:"store"`
	// Providers mirrors a listing, so a partial answer is visible as such.
	Providers []statusRow `json:"providers"`
	// New holds the models that were not in the store when this run started,
	// after the free rules were applied.
	New []modelRow `json:"new"`
	// Gone holds models the store recorded that a provider no longer offers.
	Gone []goneRow `json:"gone"`
	// Saved reports whether the store was written, rather than leaving the
	// caller to tell from the next run finding nothing.
	Saved bool `json:"saved"`
}

type goneRow struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// runDiff implements "getfm diff".
//
// It answers what the providers are offering that the last run did not, then
// records the catalog it just fetched. A provider that could not be reached is
// skipped entirely, so its models are never reported as withdrawn because a
// network was having a bad minute.
func runDiff(args []string) int {
	fs := newFlagSet("getfm diff")
	var g globals
	g.register(fs)
	var f filterFlags
	f.register(fs)
	var out outputFlags
	out.register(fs)

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
	// An unreadable store is a warning rather than a stop: the comparison is
	// still worth printing, it just cannot tell what is new.
	remembered, err := store.Load(storePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "getfm: %v; every model will be reported as new and the record replaced\n", err)
	}

	doc := diffDoc{
		Store:     storePath,
		Providers: statuses(run, sel),
		New:       []modelRow{},
		Gone:      []goneRow{},
	}
	now := time.Now().UTC()
	for _, res := range run.results {
		if res.Err != nil {
			continue
		}
		fresh, gone := recordDiff(remembered, sel, res, now)
		doc.New = append(doc.New, fresh...)
		doc.Gone = append(doc.Gone, gone...)
	}

	// Providers are folded in fetch order, so the withdrawn list is
	// sorted once here rather than per provider, which would leave it
	// grouped by whoever answered first.
	sort.Slice(doc.Gone, func(i, j int) bool {
		if doc.Gone[i].Provider != doc.Gone[j].Provider {
			return doc.Gone[i].Provider < doc.Gone[j].Provider
		}
		return doc.Gone[i].Model < doc.Gone[j].Model
	})

	// The record is rewritten only when the catalog actually moved, so a
	// no-change run leaves the store's timestamps alone.
	doc.Saved = len(doc.New) > 0 || len(doc.Gone) > 0

	// Printed before the record is written, so a failed write cannot
	// leave a record the user never saw.
	if err := writeDiffDoc(format, doc); err != nil {
		return fail(err)
	}
	if doc.Saved {
		if err := remembered.Save(storePath); err != nil {
			return fail(fmt.Errorf("save the model store: %w", err))
		}
	}

	if format != formatJSON {
		printProviderProblems(run.failures())
	}
	if !out.quiet {
		if doc.Saved {
			fmt.Fprintf(os.Stderr, "\n%d new, %d no longer listed, recorded in %s.\n",
				len(doc.New), len(doc.Gone), storePath)
		} else {
			fmt.Fprintf(os.Stderr, "\n%d new, %d no longer listed; nothing changed, %s left as is.\n",
				len(doc.New), len(doc.Gone), storePath)
		}
	}

	// The same rule as list: nothing reachable at all is a failure, whatever
	// the document says.
	if len(run.results) > 0 && len(run.failures()) == len(run.results) {
		return exitError
	}
	return exitOK
}

// recordDiff folds one provider's freshly fetched catalog into the store and
// returns what that made new and what it made gone.
//
// Both are measured against the record as it was loaded, which Record replaces
// wholesale. The whole catalog is recorded, not just the free subset, so that
// changing the free rules cannot make every model look new again.
func recordDiff(s *store.Store, sel selection, res provider.FetchResult, now time.Time) (fresh []modelRow, gone []goneRow) {
	ids := make([]string, 0, len(res.Models))
	byID := make(map[string]provider.Model, len(res.Models))
	for _, m := range res.Models {
		ids = append(ids, m.ID)
		byID[m.ID] = m
	}

	// Known has to be read before Record overwrites the entry it describes.
	known := s.Known(res.Provider)
	added := s.Record(res.Provider, ids, now)

	free := make(map[string]bool, len(sel.models[res.Provider]))
	for _, m := range sel.models[res.Provider] {
		free[m.ID] = true
	}
	for _, id := range added {
		// Only the free subset is reported, since that is what this tool is
		// for. The id is recorded either way.
		if !free[id] {
			continue
		}
		row := modelFrom(byID[id])
		row.New = true
		fresh = append(fresh, row)
	}

	for _, id := range ids {
		delete(known, id)
	}
	for id := range known {
		gone = append(gone, goneRow{Provider: res.Provider, Model: id})
	}
	return fresh, gone
}

// writeDiffDoc renders the comparison in the requested format.
func writeDiffDoc(format string, doc diffDoc) error {
	switch format {
	case formatJSON:
		return writeJSON(doc)
	case formatCSV:
		return writeDiffCSV(os.Stdout, doc)
	case formatMD:
		return writeDiffMarkdown(os.Stdout, doc)
	default:
		printDiffTable(doc)
		return nil
	}
}

// writeDiffCSV writes the comparison as one table. The state column is
// what keeps both halves in a single document: a script can tell a
// model that appeared from one that disappeared without parsing two
// files, and a withdrawn model has no pricing left to report.
func writeDiffCSV(w io.Writer, doc diffDoc) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"provider", "model", "state",
		"context_length", "prompt_price_per_1m", "completion_price_per_1m", "free"}); err != nil {
		return err
	}
	for _, r := range doc.New {
		record := []string{
			r.Provider,
			r.Model,
			"new",
			strconv.FormatInt(r.Context, 10),
			csvPrice(r.Priced, r.PromptPrice),
			csvPrice(r.Priced, r.ComplPrice),
			r.Reason,
		}
		if err := cw.Write(record); err != nil {
			return err
		}
	}
	for _, g := range doc.Gone {
		if err := cw.Write([]string{g.Provider, g.Model, "gone", "", "", "", ""}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// printDiffTable shows the new models, then the withdrawn ones. Both
// halves go to stdout, so a pipe sees the whole comparison; only the
// counts stay on stderr.
func printDiffTable(doc diffDoc) {
	if len(doc.New) == 0 {
		fmt.Println("No model is new since the last run.")
	} else {
		printModelTable(doc.New)
	}
	if len(doc.Gone) > 0 {
		fmt.Printf("\n%d no longer listed:\n", len(doc.Gone))
		for _, g := range doc.Gone {
			fmt.Printf("  %s/%s\n", g.Provider, g.Model)
		}
	}
}

func writeDiffMarkdown(w io.Writer, doc diffDoc) error {
	var b strings.Builder
	b.WriteString("# Catalog changes\n\n")
	if len(doc.New) == 0 {
		b.WriteString("No model is new since the last run.\n\n")
	} else {
		b.WriteString(fmt.Sprintf("%d new model(s).\n\n", len(doc.New)))
		b.WriteString("| provider | model | context | $/1M in | $/1M out | free | new |\n")
		b.WriteString("| --- | --- | --- | --- | --- | --- | --- |\n")
		for _, r := range doc.New {
			b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s | %s | %s |\n",
				mdCell(r.Provider), mdCell(r.Model), formatContext(r.Context),
				formatPrice(r.Priced, r.PromptPrice), formatPrice(r.Priced, r.ComplPrice),
				mdCell(r.Reason), "new"))
		}
		b.WriteString("\n")
	}

	if len(doc.Gone) > 0 {
		b.WriteString(fmt.Sprintf("## No longer listed (%d)\n\n", len(doc.Gone)))
		for _, g := range doc.Gone {
			b.WriteString(fmt.Sprintf("- %s/%s\n", mdCell(g.Provider), mdCell(g.Model)))
		}
		b.WriteString("\n")
	}

	if doc.Saved {
		b.WriteString(fmt.Sprintf("Recorded in `%s`.\n", doc.Store))
	} else {
		b.WriteString(fmt.Sprintf("Record left as is at `%s`.\n", doc.Store))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// onlyNew narrows a listing to the models the store had not recorded.
//
// It is read only. A listing that consumed its own novelty would make
// the next -new-only call come back empty, which reads as "nothing
// changed" rather than as "you already saw this".
func onlyNew(path string, run *catalogRun, sel selection) ([]modelRow, error) {
	remembered, err := store.Load(path)
	if err != nil {
		return nil, fmt.Errorf("read the model store: %w", err)
	}

	// Providers are visited in fetch order, which is the order collect
	// used, so the narrowed listing reads in the same order a plain
	// listing would.
	var rows []modelRow
	for _, res := range run.results {
		known := remembered.Known(res.Provider)
		for _, m := range sel.models[res.Provider] {
			if known[m.ID] {
				continue
			}
			row := modelFrom(m)
			row.New = true
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// printNewSummary is the -new-only counterpart of printSummary: it
// counts what was listed rather than what the catalog holds, and names
// the record the comparison was made against so a script can find it.
func printNewSummary(sel selection, listed int, storePath string) {
	total := 0
	for _, models := range sel.models {
		total += len(models)
	}
	fmt.Fprintf(os.Stderr, "\n%d new of %d free models across %d providers; record at %s.\n",
		listed, total, len(sel.models), storePath)
}
