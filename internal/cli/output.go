package cli

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/bluespada/getfm/pkg/free"
	"github.com/bluespada/getfm/pkg/provider"
)

// Output formats accepted by -format.
const (
	formatTable = "table"
	formatCSV   = "csv"
	formatMD    = "md"
	formatJSON  = "json"
)

// outputFlags control how a command writes its results.
//
// Every format writes the same document to stdout, so a caller that learned to
// read one can read the others. The stderr side (warnings, the summary) is
// separate, which is what makes -q safe to combine with any of them.
type outputFlags struct {
	format string
	asJSON bool
	quiet  bool
}

func (o *outputFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&o.format, "format", formatTable, "output format: table, csv, md or json")
	fs.BoolVar(&o.asJSON, "json", false, "emit JSON instead of a table, the same as -format=json")
	fs.BoolVar(&o.quiet, "q", false, "suppress the closing summary line")
}

// resolve settles -json against -format, since either flag can ask for JSON.
// Asking for two different shapes at once is a usage error rather than a silent
// preference for one of them.
func (o *outputFlags) resolve() (string, error) {
	format := strings.ToLower(strings.TrimSpace(o.format))
	if o.asJSON {
		if format != "" && format != formatTable && format != formatJSON {
			return "", fmt.Errorf("-json conflicts with -format=%s", o.format)
		}
		format = formatJSON
	}
	switch format {
	case formatTable, formatCSV, formatMD, formatJSON:
		return format, nil
	}
	return "", fmt.Errorf("unknown -format %q: use %s", o.format, strings.Join([]string{formatTable, formatCSV, formatMD, formatJSON}, ", "))
}

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
	// New marks a model the store had not recorded, which only the -new-only
	// listing and diff ever set.
	New bool `json:"new,omitempty"`
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

// writeListDoc renders a model listing in the requested format.
func writeListDoc(format string, doc listDoc) error {
	switch format {
	case formatJSON:
		if doc.Models == nil {
			// An empty list marshals as null otherwise, which a caller reading
			// the document as an array cannot iterate.
			doc.Models = []modelRow{}
		}
		return writeJSON(doc)
	case formatCSV:
		return writeModelCSV(os.Stdout, doc.Models)
	case formatMD:
		return writeModelMarkdown(os.Stdout, doc)
	default:
		printModelTable(doc.Models)
		return nil
	}
}

// writeProbeDoc renders probe results in the requested format.
func writeProbeDoc(format string, doc probeDoc) error {
	switch format {
	case formatJSON:
		if doc.Results == nil {
			doc.Results = []probeRow{}
		}
		return writeJSON(doc)
	case formatCSV:
		return writeProbeCSV(os.Stdout, doc.Results)
	case formatMD:
		return writeProbeMarkdown(os.Stdout, doc)
	default:
		printProbeTable(doc.Results)
		return nil
	}
}

func newTable() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
}

// writeModelCSV writes one row per model. Numbers stay numeric: the point of the
// format is that awk or a spreadsheet can add them up, so a price is not
// formatted as "$1.00" and an unpriced model is an empty cell rather than "-".
func writeModelCSV(w io.Writer, rows []modelRow) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"provider", "model", "name", "context_length",
		"prompt_price_per_1m", "completion_price_per_1m", "free_reason", "new"}); err != nil {
		return err
	}
	for _, r := range rows {
		record := []string{
			r.Provider,
			r.Model,
			r.Name,
			strconv.FormatInt(r.Context, 10),
			csvPrice(r.Priced, r.PromptPrice),
			csvPrice(r.Priced, r.ComplPrice),
			r.Reason,
			strconv.FormatBool(r.New),
		}
		if err := cw.Write(record); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func csvPrice(priced bool, v float64) string {
	if !priced {
		return ""
	}
	return strconv.FormatFloat(v*1e6, 'f', -1, 64)
}

// writeModelMarkdown renders the listing as a document: the free rules in force,
// the models, and then which providers answered, so the counts can be checked
// against the sources they came from.
func writeModelMarkdown(w io.Writer, doc listDoc) error {
	var b strings.Builder
	b.WriteString("# Free models\n\n")
	b.WriteString(fmt.Sprintf("Free rules: `%s`", doc.Free.Rules))
	if doc.Free.Suffix != "" {
		b.WriteString(fmt.Sprintf(" with suffix `%s`", doc.Free.Suffix))
	}
	if len(doc.Free.Allow) > 0 {
		b.WriteString(fmt.Sprintf(", allowing %s", mdList(doc.Free.Allow)))
	}
	b.WriteString(".\n\n")

	b.WriteString("| provider | model | context | $/1M in | $/1M out | free | new |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, r := range doc.Models {
		flag := "-"
		if r.New {
			flag = "new"
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s | %s | %s |\n",
			mdCell(r.Provider), mdCell(r.Model), formatContext(r.Context),
			formatPrice(r.Priced, r.PromptPrice), formatPrice(r.Priced, r.ComplPrice),
			mdCell(r.Reason), flag))
	}
	b.WriteString("\n")

	b.WriteString("## Providers\n\n")
	b.WriteString("| provider | ok | models | free | elapsed ms |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, p := range doc.Providers {
		state := "yes"
		if !p.OK {
			state = "no"
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %d | %d | %d |\n",
			mdCell(p.Provider), state, p.Models, p.Free, p.ElapsedMS))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// writeProbeCSV writes one row per probe.
func writeProbeCSV(w io.Writer, rows []probeRow) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"provider", "model", "ok", "status", "reason",
		"latency_ms", "tokens", "detail"}); err != nil {
		return err
	}
	for _, r := range rows {
		status := ""
		if r.Status != 0 {
			// A status of zero means the request never completed, so a literal
			// 0 would read as a real response code.
			status = strconv.Itoa(r.Status)
		}
		record := []string{
			r.Provider,
			r.Model,
			strconv.FormatBool(r.OK),
			status,
			r.Reason,
			strconv.FormatFloat(r.Latency, 'f', -1, 64),
			strconv.FormatInt(r.Tokens, 10),
			r.Detail,
		}
		if err := cw.Write(record); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// writeProbeMarkdown renders probe results as a document: the summary,
// one row per probe in the order they ran, then the captured bodies of
// the failed ones, which are what a failure is diagnosed from.
func writeProbeMarkdown(w io.Writer, doc probeDoc) error {
	var b strings.Builder
	b.WriteString("# Probe results\n\n")
	b.WriteString(fmt.Sprintf("%d of %d endpoints responded successfully.\n\n",
		doc.OK, doc.Total))

	b.WriteString("| provider | model | result | reason | latency | tokens |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	for _, r := range doc.Results {
		result := fmt.Sprintf("%d", r.Status)
		if r.Status == 0 {
			result = "no response"
		}
		reason := r.Reason
		if reason == "" {
			reason = "-"
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s | %s |\n",
			mdCell(r.Provider), mdCell(r.Model), result, mdCell(reason),
			fmt.Sprintf("%.0fms", r.Latency), formatTokens(r.Tokens)))
	}
	b.WriteString("\n")

	// The captured bodies are what a failure is actually diagnosed from, so
	// they are kept as a detail section rather than dropped for being prose.
	var failed []probeRow
	for _, r := range doc.Results {
		if !r.OK && r.Detail != "" {
			failed = append(failed, r)
		}
	}
	if len(failed) == 0 {
		_, err := io.WriteString(w, b.String())
		return err
	}
	b.WriteString("## Failures\n\n")
	for _, r := range failed {
		b.WriteString(fmt.Sprintf("- **%s/%s** (%s): %s\n",
			mdCell(r.Provider), mdCell(r.Model), r.Reason, mdCell(r.Detail)))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// mdCell escapes the characters that would otherwise break a table row. A model
// name carrying a pipe is common enough across provider catalogues to be worth
// handling, and a newline inside a cell would silently end the row.
func mdCell(s string) string {
	s = strings.NewReplacer("|", "\\|", "\r", " ", "\n", " ").Replace(s)
	return strings.TrimSpace(s)
}

// mdList renders a short list inline for prose.
func mdList(items []string) string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = "`" + s + "`"
	}
	return strings.Join(out, ", ")
}

// printModelTable renders the free model listing.
func printModelTable(rows []modelRow) {
	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "No free models matched the current rules.")
		return
	}
	t := newTable()
	fmt.Fprintln(t, "PROVIDER\tMODEL\tCONTEXT\t$/1M IN\t$/1M OUT\tFREE\tNEW")
	for _, r := range rows {
		flag := "-"
		if r.New {
			flag = "new"
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.Provider, r.Model, formatContext(r.Context),
			formatPrice(r.Priced, r.PromptPrice), formatPrice(r.Priced, r.ComplPrice),
			r.Reason, flag)
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

// modelFrom converts one catalog entry into its output row.
func modelFrom(m provider.Model) modelRow {
	return modelRow{
		Provider:    m.Provider,
		Model:       m.ID,
		Name:        m.Name,
		Context:     m.Context,
		PromptPrice: m.PromptPrice,
		ComplPrice:  m.CompletionPrice,
		Priced:      m.Priced,
		Reason:      m.Reason,
	}
}

// rowsFrom converts filtered models into JSON rows.
func rowsFrom(models []provider.Model) []modelRow {
	rows := make([]modelRow, 0, len(models))
	for _, m := range models {
		rows = append(rows, modelFrom(m))
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
