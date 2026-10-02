package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/bluespada/getfm/pkg/free"
	"github.com/bluespada/getfm/pkg/provider"
)

// filterFlags are the free-model selection flags, shared by list and test.
type filterFlags struct {
	spec   string
	suffix string
	allow  string
	deny   string
}

func (f *filterFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.spec, "free", "all", "free rules to apply: all, suffix, zero, none, or a comma separated combination")
	fs.StringVar(&f.suffix, "suffix", free.DefaultSuffix, "model id suffix that marks a free variant")
	fs.StringVar(&f.allow, "allow", "", "comma separated model ids to treat as free regardless of pricing")
	fs.StringVar(&f.deny, "deny", "", "comma separated model ids to exclude")
}

// rules parses the filter flags into free.Rules.
func (f *filterFlags) rules() (rules, error) { return parseRules(f.spec, f.suffix, f.allow, f.deny) }

// modelRow is the JSON shape of one listed model.
type modelRow struct {
	Provider    string  `json:"provider"`
	Model       string  `json:"model"`
	Name        string  `json:"name,omitempty"`
	Context     int64   `json:"context_length,omitempty"`
	PromptPrice float64 `json:"prompt_price"`
	ComplPrice  float64 `json:"completion_price"`
	Priced      bool    `json:"priced"`
	Reason      string  `json:"free_reason"`
}

// listDoc is the JSON document emitted by list.
type listDoc struct {
	Free      filterDoc   `json:"free"`
	Providers []statusRow `json:"providers"`
	Models    []modelRow  `json:"models"`
}

type filterDoc struct {
	Rules  string   `json:"rules"`
	Suffix string   `json:"suffix,omitempty"`
	Allow  []string `json:"allow,omitempty"`
}

type statusRow struct {
	Provider  string `json:"provider"`
	OK        bool   `json:"ok"`
	Models    int    `json:"models"`
	Free      int    `json:"free"`
	ElapsedMS int64  `json:"elapsed_ms"`
	Error     string `json:"error,omitempty"`
}

func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func newTable() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
}

// printModelTable renders the free model listing.
func printModelTable(rows []modelRow) {
	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "No free models matched the current rules.")
		return
	}
	t := newTable()
	fmt.Fprintln(t, "PROVIDER\tMODEL\tCONTEXT\t$/1M IN\t$/1M OUT\tFREE")
	for _, r := range rows {
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\t%s\n",
			r.Provider, r.Model, formatContext(r.Context),
			formatPrice(r.Priced, r.PromptPrice), formatPrice(r.Priced, r.ComplPrice), r.Reason)
	}
	t.Flush()
}

// printProviderProblems tells the user which providers could not be listed, so a
// partial result is never mistaken for a complete one.
func printProviderProblems(bad []provider.FetchResult) {
	for _, b := range bad {
		fmt.Fprintf(os.Stderr, "getfm: %s could not be listed: %v\n", b.Provider, b.Err)
	}
}

// formatPrice renders a per-token price as a per-million-token figure. A model
// whose provider published no pricing shows as "-" rather than "$0.00", because
// unknown and free are not the same thing.
func formatPrice(priced bool, v float64) string {
	if !priced {
		return "-"
	}
	return fmt.Sprintf("$%.2f", v*1e6)
}

func formatContext(n int64) string {
	switch {
	case n <= 0:
		return "-"
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%dk", n/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// splitModelRef splits "provider/model" on the first slash, since model ids
// themselves contain slashes: "openrouter/qwen/qwen3:free" is provider
// "openrouter" and model "qwen/qwen3:free".
func splitModelRef(s string) (provider, model string, ok bool) {
	provider, model, ok = strings.Cut(s, "/")
	if !ok || provider == "" || model == "" {
		return "", s, false
	}
	return provider, model, true
}

// rowsFrom converts filtered models into JSON rows.
func rowsFrom(models []provider.Model) []modelRow {
	rows := make([]modelRow, 0, len(models))
	for _, m := range models {
		rows = append(rows, modelRow{
			Provider:    m.Provider,
			Model:       m.ID,
			Name:        m.Name,
			Context:     m.Context,
			PromptPrice: m.PromptPrice,
			ComplPrice:  m.CompletionPrice,
			Priced:      m.Priced,
			Reason:      m.Reason,
		})
	}
	return rows
}

func filterDocFrom(spec, suffix, allow string) filterDoc {
	return filterDoc{Rules: spec, Suffix: suffix, Allow: splitCSV(allow)}
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
