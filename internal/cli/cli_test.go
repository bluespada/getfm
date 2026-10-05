package cli

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bluespada/getfm/pkg/config"
	"github.com/bluespada/getfm/pkg/provider"
	"github.com/bluespada/getfm/pkg/store"
)

// The command line is tested through Main, against a local httptest server, so
// nothing here touches the network or a real provider. Output is captured by
// swapping the process streams; every command writes to them directly.

// modelsPayload covers each way a model can qualify as free: one paid model
// carrying the suffix, one published as free by price and nothing else, and one
// plainly paid. The suffix model is deliberately not free by price, so the two
// rules can be told apart.
const modelsPayload = `{"data":[
	{"id":"free-model:free","name":"Free Model","context_length":8192,"pricing":{"prompt":"0.000001","completion":"0.000002"}},
	{"id":"paid-model","name":"Paid Model","context_length":4096,"pricing":{"prompt":"0.000001","completion":"0.000002"}},
	{"id":"zero-model","name":"Zero Priced","context_length":2048,"pricing":{"prompt":"0","completion":"0"}}
]}`

// runMain runs the command line with both streams captured.
func runMain(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	code = Main(append([]string{"getfm"}, args...))
	os.Stdout, os.Stderr = origOut, origErr

	if err := outW.Close(); err != nil {
		t.Fatal(err)
	}
	if err := errW.Close(); err != nil {
		t.Fatal(err)
	}
	return code, readFile(t, outR), readFile(t, errR)
}

func readFile(t *testing.T, f *os.File) string {
	t.Helper()
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// gatewayServer serves a models endpoint and, when the payload is not empty, a
// chat completions endpoint. completions says what that endpoint answers.
func gatewayServer(t *testing.T, models string, completions func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			if _, err := io.WriteString(w, models); err != nil {
				t.Error(err)
			}
		case "/chat/completions":
			if completions == nil {
				http.NotFound(w, r)
				return
			}
			completions(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// catalogProvider is one entry of providers.json, spelled out so a test states
// the parts it cares about rather than repeating the whole schema.
func catalogProvider(baseURL string, edit func(map[string]any)) map[string]any {
	p := map[string]any{
		"name":        "alpha",
		"label":       "Alpha",
		"base_url":    baseURL,
		"models_path": "/models",
		"env_keys":    []string{"ALPHA_KEY"},
		"mapping": map[string]any{
			"list":             "data",
			"id":               "id",
			"name":             "name",
			"context":          "context_length",
			"prompt_price":     "pricing.prompt",
			"completion_price": "pricing.completion",
		},
		"completions": map[string]any{
			"path":        "/chat/completions",
			"auth_header": "Authorization",
			"auth_prefix": "Bearer ",
			"body":        `{"model":"{{.Model}}","max_tokens":1}`,
			"usage":       "usage.total_tokens",
		},
	}
	if edit != nil {
		edit(p)
	}
	return p
}

// writeCatalog writes providers.json into a temporary directory and returns its
// path, ready to be passed to -config.
func writeCatalog(t *testing.T, providers ...map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"providers": providers})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUnknownCommandIsAUsageError(t *testing.T) {
	code, _, stderr := runMain(t, "bogus")
	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, `unknown command "bogus"`) {
		t.Errorf("stderr = %q, want it to name the bad command", stderr)
	}
}

func TestHelpAndVersionExitCleanly(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		code, stdout, _ := runMain(t, arg)
		if code != exitOK {
			t.Errorf("%s: exit code = %d, want %d", arg, code, exitOK)
		}
		if !strings.Contains(stdout, "Usage:") {
			t.Errorf("%s: stdout does not show the usage text", arg)
		}
	}
	code, stdout, _ := runMain(t, "version")
	if code != exitOK || !strings.Contains(stdout, version) {
		t.Errorf("version printed %q with code %d", stdout, code)
	}
}

// TestLeadingFlagReachesTheBrowser covers `getfm -providers x`, which used to
// die as an unknown command called "-providers". Without a terminal the
// browser falls back to the listing, which is what can be asserted here.
func TestLeadingFlagReachesTheBrowser(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, nil)
	path := writeCatalog(t, catalogProvider(srv.URL, nil))

	code, stdout, stderr := runMain(t, "-config", path, "-providers", "alpha")
	if code != exitOK {
		t.Errorf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	if strings.Contains(stderr, "unknown command") {
		t.Errorf("a leading flag was read as a command: %s", stderr)
	}
	if !strings.Contains(stdout, "free-model:free") {
		t.Errorf("stdout does not list the free model:\n%s", stdout)
	}
}

func TestListDefaultsToTheSuffixAndZeroRules(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, nil)
	path := writeCatalog(t, catalogProvider(srv.URL, nil))

	code, stdout, stderr := runMain(t, "list", "-config", path)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	for _, want := range []string{"free-model:free", "zero-model", "suffix", "zero-price"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table is missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "paid-model") {
		t.Errorf("a paid model was listed as free:\n%s", stdout)
	}
}

// TestListOnlyZeroPriceRule is the flag-level form of the free rules bug: with
// -free zero, a model that merely carries the :free suffix is not free.
func TestListOnlyZeroPriceRule(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, nil)
	path := writeCatalog(t, catalogProvider(srv.URL, nil))

	code, stdout, stderr := runMain(t, "list", "-config", path, "-free", "zero")
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "zero-model") {
		t.Errorf("the zero priced model should be listed:\n%s", stdout)
	}
	if strings.Contains(stdout, "free-model:free") {
		t.Errorf("-free zero still applied the suffix rule:\n%s", stdout)
	}
}

func TestListJSONIsAMachineReadableDocument(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, nil)
	path := writeCatalog(t, catalogProvider(srv.URL, nil))

	code, stdout, stderr := runMain(t, "list", "-json", "-config", path)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	// Unmarshal rejects anything appended after the document, so this also
	// proves stdout stayed pure JSON.
	var doc listDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	if doc.Free.Rules != "all" || doc.Free.Suffix != ":free" {
		t.Errorf("free rules echoed as %+v, want the defaults", doc.Free)
	}
	if len(doc.Providers) != 1 || !doc.Providers[0].OK || doc.Providers[0].Models != 3 {
		t.Errorf("providers = %+v, want one healthy provider with 3 models", doc.Providers)
	}
	if len(doc.Models) != 2 {
		t.Fatalf("models = %+v, want the 2 free ones", doc.Models)
	}
	if got := doc.Models[0].Model; got != "free-model:free" {
		t.Errorf("models are not sorted by id: first is %q", got)
	}
	if !doc.Models[0].Priced || doc.Models[0].Reason != "suffix" {
		t.Errorf("first model = %+v", doc.Models[0])
	}
}

// TestListInjectsTheEnvKeyIntoDeclaredHeaders covers the whole path a catalog
// takes: env_keys resolves a credential, the header template consumes it, and
// the models request arrives with it. A provider whose catalog is gated cannot
// be listed without this.
func TestListInjectsTheEnvKeyIntoDeclaredHeaders(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-list-key-value" {
			t.Errorf("auth header = %q, want the env key injected", got)
		}
	})
	path := writeCatalog(t, catalogProvider(srv.URL, func(p map[string]any) {
		p["headers"] = map[string]any{"Authorization": "Bearer {{.Key}}"}
	}))
	t.Setenv("ALPHA_KEY", "sk-list-key-value")

	code, stdout, stderr := runMain(t, "list", "-config", path)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "free-model:free") {
		t.Errorf("stdout is missing the listed models:\n%s", stdout)
	}
}

// TestListSendsNoAuthWhenTheCatalogDeclaresNone pins the other half: a
// provider that declares no headers gets a bare models request even when a key
// is available, because most models endpoints are public.
func TestListSendsNoAuthWhenTheCatalogDeclaresNone(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("undeclared auth header = %q, want none", got)
		}
	})
	path := writeCatalog(t, catalogProvider(srv.URL, nil))
	t.Setenv("ALPHA_KEY", "sk-list-key-value")

	if code, _, stderr := runMain(t, "list", "-config", path); code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
}

func TestListFailsWhenEveryProviderFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream is unwell", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	path := writeCatalog(t, catalogProvider(srv.URL, nil))

	code, _, stderr := runMain(t, "list", "-config", path)
	if code != exitError {
		t.Errorf("exit code = %d, want %d when nothing could be listed", code, exitError)
	}
	if !strings.Contains(stderr, "could not be listed") {
		t.Errorf("stderr = %q, want it to say the provider failed", stderr)
	}
}

func TestListSucceedsWhenOneProviderAnswers(t *testing.T) {
	good := gatewayServer(t, modelsPayload, nil)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)

	first := catalogProvider(good.URL, nil)
	second := catalogProvider(bad.URL, func(p map[string]any) { p["name"] = "beta" })
	path := writeCatalog(t, first, second)

	code, stdout, stderr := runMain(t, "list", "-config", path)
	if code != exitOK {
		t.Errorf("exit code = %d, want %d while one provider still answers", code, exitOK)
	}
	if !strings.Contains(stdout, "free-model:free") {
		t.Errorf("stdout is missing the healthy provider's models:\n%s", stdout)
	}
	if !strings.Contains(stderr, "beta could not be listed") {
		t.Errorf("stderr = %q, want the failing provider named", stderr)
	}
	if !strings.Contains(stderr, "2 free of 3 models across 1 providers") {
		t.Errorf("summary = %q, want it to count only what answered", stderr)
	}
}

func TestTestCommandProbesAModel(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test-key-value" {
			t.Errorf("auth header = %q", got)
		}
		if _, err := io.WriteString(w, `{"usage":{"total_tokens":7}}`); err != nil {
			t.Error(err)
		}
	})
	path := writeCatalog(t, catalogProvider(srv.URL, nil))
	t.Setenv("ALPHA_KEY", "sk-test-key-value")

	code, stdout, stderr := runMain(t, "test", "-config", path, "alpha/free-model:free")
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	for _, want := range []string{"free-model:free", "200", "7"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("probe table is missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "1 of 1 endpoints responded successfully") {
		t.Errorf("summary = %q", stderr)
	}
}

func TestTestCommandFailsWhenTheEndpointRefuses(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		// Echo the credential back, which is what makes redaction matter.
		if _, err := io.WriteString(w, `{"error":"bad key `+r.Header.Get("Authorization")+`"}`); err != nil {
			t.Error(err)
		}
	})
	path := writeCatalog(t, catalogProvider(srv.URL, nil))
	t.Setenv("ALPHA_KEY", "sk-test-key-value")

	code, stdout, stderr := runMain(t, "test", "-config", path, "alpha/free-model:free")
	if code != exitError {
		t.Errorf("exit code = %d, want %d for a failed probe", code, exitError)
	}
	if !strings.Contains(stdout, "401") {
		t.Errorf("stdout should report the status:\n%s", stdout)
	}
	if strings.Contains(stdout+stderr, "sk-test-key-value") {
		t.Errorf("the credential reached the output:\n%s%s", stdout, stderr)
	}
	if !strings.Contains(stdout, "[redacted]") {
		t.Errorf("the echoed credential was not marked as redacted:\n%s", stdout)
	}
}

func TestTestCommandWarnsAboutAMissingKey(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("no key was configured but the header was %q", got)
		}
		if _, err := io.WriteString(w, `{}`); err != nil {
			t.Error(err)
		}
	})
	path := writeCatalog(t, catalogProvider(srv.URL, nil))
	t.Setenv("ALPHA_KEY", "")

	_, _, stderr := runMain(t, "test", "-config", path, "alpha/free-model:free")
	if !strings.Contains(stderr, "no API key for alpha") {
		t.Errorf("stderr = %q, want the missing key called out", stderr)
	}
}

func TestTestAllHonoursTheLimit(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.WriteString(w, `{"usage":{"total_tokens":1}}`); err != nil {
			t.Error(err)
		}
	})
	path := writeCatalog(t, catalogProvider(srv.URL, nil))
	t.Setenv("ALPHA_KEY", "sk-test-key-value")

	code, stdout, _ := runMain(t, "test", "-all", "-n", "1", "-config", path)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	rows := 0
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "alpha") {
			rows++
		}
	}
	if rows != 1 {
		t.Errorf("probed %d models, want 1 with -n 1:\n%s", rows, stdout)
	}
}

func TestTestJSONReportsEachProbe(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.WriteString(w, `{"usage":{"total_tokens":5}}`); err != nil {
			t.Error(err)
		}
	})
	path := writeCatalog(t, catalogProvider(srv.URL, nil))
	t.Setenv("ALPHA_KEY", "sk-test-key-value")

	code, stdout, stderr := runMain(t, "test", "-all", "-json", "-config", path)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	var doc probeDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	if doc.Total != 2 || doc.OK != 2 || doc.Failed != 0 {
		t.Errorf("doc = %+v, want 2 successful probes", doc)
	}
	if doc.Results[0].Tokens != 5 || doc.Results[0].Latency <= 0 {
		t.Errorf("first result = %+v, want tokens and a latency", doc.Results[0])
	}
}

func TestTestRejectsBadArguments(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, nil)
	path := writeCatalog(t, catalogProvider(srv.URL, nil))

	cases := map[string][]string{
		"two models":        {"test", "-config", path, "alpha/a", "alpha/b"},
		"unknown bare id":   {"test", "-config", path, "no-such-model"},
		"unknown free rule": {"list", "-config", path, "-free", "magic"},
		"unknown flag":      {"list", "-config", path, "-nope"},
		"missing config":    {"list", "-config", filepath.Join(t.TempDir(), "absent.json")},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			code, _, _ := runMain(t, args...)
			if code == exitOK {
				t.Errorf("%v succeeded, want a non-zero exit", args)
			}
		})
	}
}

// seedStore writes a store holding the given ids per provider, so a command
// that compares against history can be tested without one.
func seedStore(t *testing.T, path string, known map[string][]string) {
	t.Helper()
	now := time.Now().UTC()
	s := &store.Store{Version: store.Version, Models: map[string]store.ProviderRecord{}}
	for name, ids := range known {
		seen := make(map[string]time.Time, len(ids))
		for _, id := range ids {
			seen[id] = now
		}
		s.Models[name] = store.ProviderRecord{Updated: now, Seen: seen}
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
}

// storedIDs reads back what a store recorded, for asserting that a command
// wrote, or did not write, it.
func storedIDs(t *testing.T, path, providerName string) map[string]bool {
	t.Helper()
	s, err := store.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return s.Known(providerName)
}

func TestListCSVIsReadableByACSVParser(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, nil)
	path := writeCatalog(t, catalogProvider(srv.URL, nil))

	code, stdout, stderr := runMain(t, "list", "-format=csv", "-config", path)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	rows, err := csv.NewReader(strings.NewReader(stdout)).ReadAll()
	if err != nil {
		t.Fatalf("stdout is not one CSV table: %v\n%s", err, stdout)
	}
	want := []string{"provider", "model", "name", "context_length",
		"prompt_price_per_1m", "completion_price_per_1m", "free_reason", "new"}
	if len(rows) != 3 || strings.Join(rows[0], ",") != strings.Join(want, ",") {
		t.Fatalf("header = %v, want %v", rows[0], want)
	}
	// A price has to stay a number, or the format is no use to a spreadsheet.
	if rows[1][4] != "1" || rows[1][5] != "2" {
		t.Errorf("price columns = %q and %q, want 1 and 2 per million", rows[1][4], rows[1][5])
	}
	if rows[1][1] != "free-model:free" || rows[1][6] != "suffix" {
		t.Errorf("first row = %v, want the free model by id", rows[1])
	}
}

func TestListMarkdownIsADocument(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, nil)
	path := writeCatalog(t, catalogProvider(srv.URL, nil))

	code, stdout, stderr := runMain(t, "list", "-format=md", "-config", path)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	for _, want := range []string{"# Free models", "| provider | model |", "free-model:free", "## Providers"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("markdown is missing %q:\n%s", want, stdout)
		}
	}
}

func TestFormatJSONMatchesTheJSONFlag(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, nil)
	path := writeCatalog(t, catalogProvider(srv.URL, nil))

	_, viaFlag, _ := runMain(t, "list", "-json", "-config", path)
	_, viaFormat, _ := runMain(t, "list", "-format=json", "-config", path)

	// Compared field by field rather than as text: the two runs fetch
	// separately, so the elapsed time in the document differs either way.
	var a, b listDoc
	if err := json.Unmarshal([]byte(viaFlag), &a); err != nil {
		t.Fatalf("-json did not produce one document: %v\n%s", err, viaFlag)
	}
	if err := json.Unmarshal([]byte(viaFormat), &b); err != nil {
		t.Fatalf("-format=json did not produce one document: %v\n%s", err, viaFormat)
	}
	if !reflect.DeepEqual(a.Models, b.Models) || !reflect.DeepEqual(a.Free, b.Free) {
		t.Errorf("-json gave %+v, -format=json gave %+v, want the same document", a, b)
	}
}

func TestQuietDropsTheSummaryButKeepsTheRows(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, nil)
	path := writeCatalog(t, catalogProvider(srv.URL, nil))

	code, stdout, stderr := runMain(t, "list", "-q", "-config", path)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "free-model:free") {
		t.Errorf("-q dropped the rows:\n%s", stdout)
	}
	if strings.Contains(stderr, "free of") {
		t.Errorf("-q kept the summary line: %q", stderr)
	}
}

func TestTestCSVCarriesTheFailureClass(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	path := writeCatalog(t, catalogProvider(srv.URL, nil))
	t.Setenv("ALPHA_KEY", "sk-test-key-value")

	code, stdout, _ := runMain(t, "test", "-all", "-format=csv", "-config", path)
	if code != exitError {
		t.Fatalf("exit code = %d, want %d for failing probes", code, exitError)
	}
	rows, err := csv.NewReader(strings.NewReader(stdout)).ReadAll()
	if err != nil {
		t.Fatalf("stdout is not one CSV table: %v\n%s", err, stdout)
	}
	reason := -1
	for i, h := range rows[0] {
		if h == "reason" {
			reason = i
		}
	}
	if reason < 0 {
		t.Fatalf("no reason column in %v", rows[0])
	}
	for _, row := range rows[1:] {
		if row[reason] != "auth" {
			t.Errorf("reason = %q, want auth for a 401:\n%v", row[reason], rows)
		}
	}
	// The probe table is a second shape of the same document, so its reason
	// column has to be present in the terminal output too.
	_, table, _ := runMain(t, "test", "-all", "-config", path)
	if !strings.Contains(table, "REASON") || !strings.Contains(table, "auth") {
		t.Errorf("table is missing the reason column:\n%s", table)
	}
}

func TestBadOutputFlagsAreUsageErrors(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, nil)
	path := writeCatalog(t, catalogProvider(srv.URL, nil))

	cases := map[string][]string{
		"unknown format":  {"list", "-config", path, "-format=xml"},
		"json and csv":    {"list", "-config", path, "-json", "-format=csv"},
		"bad test format": {"test", "-config", path, "-format=xml", "alpha/free-model:free"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			code, _, stderr := runMain(t, args...)
			if code != exitUsage {
				t.Errorf("exit code = %d, want %d", code, exitUsage)
			}
			if !strings.Contains(stderr, "format") {
				t.Errorf("stderr = %q, want it to name the flag", stderr)
			}
		})
	}
}

func TestDiffReportsNewModelsThenRecordsThem(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, nil)
	path := writeCatalog(t, catalogProvider(srv.URL, nil))
	storePath := filepath.Join(t.TempDir(), "models-store.json")

	code, stdout, stderr := runMain(t, "diff", "-config", path, "-store", storePath)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	// A first run has no history, so every free model is new, and the paid one
	// is recorded without being reported.
	if !strings.Contains(stdout, "free-model:free") || !strings.Contains(stdout, "zero-model") {
		t.Errorf("stdout does not report the new models:\n%s", stdout)
	}
	if strings.Contains(stdout, "paid-model") {
		t.Errorf("a paid model was reported as new:\n%s", stdout)
	}
	if !strings.Contains(stderr, "2 new") {
		t.Errorf("summary = %q, want it to count the two free models", stderr)
	}
	if got := storedIDs(t, storePath, "alpha"); !got["paid-model"] {
		t.Error("the store recorded only free models, so the paid one will look new forever")
	}

	// The second run measures from what the first one recorded. The
	// empty state goes to stdout with the rest of the comparison.
	code, stdout, stderr = runMain(t, "diff", "-config", path, "-store", storePath)
	if code != exitOK {
		t.Fatalf("second run exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "No model is new") {
		t.Errorf("second run = %q, want it to report nothing new", stdout)
	}
	if strings.Contains(stdout, "free-model:free") {
		t.Errorf("a model recorded by the first run was reported as new again:\n%s", stdout)
	}
}

func TestDiffReportsModelsNoLongerListed(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, nil)
	path := writeCatalog(t, catalogProvider(srv.URL, nil))
	storePath := filepath.Join(t.TempDir(), "models-store.json")
	seedStore(t, storePath, map[string][]string{"alpha": {
		"free-model:free", "zero-model", "retired-model",
	}})

	code, stdout, stderr := runMain(t, "diff", "-config", path, "-store", storePath)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "alpha/retired-model") {
		t.Errorf("stdout = %q, want the withdrawn model named", stdout)
	}
	if !strings.Contains(stderr, "1 no longer listed") {
		t.Errorf("summary = %q, want the count", stderr)
	}
	if got := storedIDs(t, storePath, "alpha"); got["retired-model"] {
		t.Error("the withdrawn model is still in the store")
	}
}

// TestDiffLeavesAFailedProviderAlone covers the case that makes a diff
// trustworthy: a provider that could not be reached must not have its catalog
// read as withdrawn.
func TestDiffLeavesAFailedProviderAlone(t *testing.T) {
	good := gatewayServer(t, modelsPayload, nil)
	path := writeCatalog(t, catalogProvider(good.URL, nil))
	storePath := filepath.Join(t.TempDir(), "models-store.json")
	seedStore(t, storePath, map[string][]string{"alpha": {
		"free-model:free", "zero-model", "paid-model",
	}})

	// The provider goes away between the two runs.
	good.Close()

	code, stdout, stderr := runMain(t, "diff", "-config", path, "-store", storePath)
	if code != exitError {
		t.Errorf("exit code = %d, want %d when nothing could be listed", code, exitError)
	}
	if strings.Contains(stdout, "alpha/free-model:free\n") || strings.Contains(stdout, "no longer listed:\n") {
		t.Errorf("an unreachable provider was read as a withdrawal:\n%s", stdout)
	}
	if !strings.Contains(stderr, "0 new, 0 no longer listed") {
		t.Errorf("summary = %q, want nothing added and nothing withdrawn", stderr)
	}
	if got := storedIDs(t, storePath, "alpha"); !got["free-model:free"] || !got["paid-model"] {
		t.Error("the store was overwritten by a fetch that failed")
	}
}

func TestDiffCSVCarriesBothHalves(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, nil)
	path := writeCatalog(t, catalogProvider(srv.URL, nil))
	storePath := filepath.Join(t.TempDir(), "models-store.json")
	seedStore(t, storePath, map[string][]string{"alpha": {
		"zero-model", "paid-model", "retired-model",
	}})

	code, stdout, stderr := runMain(t, "diff", "-format=csv", "-config", path, "-store", storePath)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	rows, err := csv.NewReader(strings.NewReader(stdout)).ReadAll()
	if err != nil {
		t.Fatalf("stdout is not one CSV table: %v\n%s", err, stdout)
	}
	if rows[0][2] != "state" {
		t.Fatalf("header = %v, want a state column", rows[0])
	}
	var fresh, withdrawn bool
	for _, row := range rows[1:] {
		switch row[2] {
		case "new":
			fresh = fresh || row[1] == "free-model:free"
		case "gone":
			withdrawn = withdrawn || row[0] == "alpha" && row[1] == "retired-model"
		}
	}
	if !fresh {
		t.Errorf("stdout = %q, want the new model with state new", stdout)
	}
	if !withdrawn {
		t.Errorf("stdout = %q, want the withdrawn model with state gone", stdout)
	}
}

func TestDiffMarkdownCarriesBothHalves(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, nil)
	path := writeCatalog(t, catalogProvider(srv.URL, nil))
	storePath := filepath.Join(t.TempDir(), "models-store.json")
	seedStore(t, storePath, map[string][]string{"alpha": {
		"zero-model", "paid-model", "retired-model",
	}})

	code, stdout, stderr := runMain(t, "diff", "-format=md", "-config", path, "-store", storePath)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "| alpha | free-model:free |") {
		t.Errorf("stdout does not show the new model:\n%s", stdout)
	}
	if !strings.Contains(stdout, "## No longer listed (1)") || !strings.Contains(stdout, "- alpha/retired-model") {
		t.Errorf("stdout does not show the withdrawn model:\n%s", stdout)
	}
}

// TestDiffNoChangeLeavesTheRecordAlone pins the save rule: a run
// where nothing moved must not rewrite the store, so the record's
// timestamps keep saying when the catalog was last seen to change.
func TestDiffNoChangeLeavesTheRecordAlone(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, nil)
	path := writeCatalog(t, catalogProvider(srv.URL, nil))
	storePath := filepath.Join(t.TempDir(), "models-store.json")

	if _, _, stderr := runMain(t, "diff", "-config", path, "-store", storePath); !strings.Contains(stderr, "recorded in") {
		t.Fatalf("first run summary = %q, want it to record the store", stderr)
	}
	before, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runMain(t, "diff", "-config", path, "-store", storePath)
	if code != exitOK {
		t.Fatalf("second run exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	if !strings.Contains(stderr, "nothing changed") {
		t.Errorf("summary = %q, want it to say the record was left alone", stderr)
	}
	if strings.Contains(stdout, "free-model:free") {
		t.Errorf("a recorded model was reported as new again:\n%s", stdout)
	}
	after, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("the store was rewritten by a run that changed nothing")
	}
}

// TestDiffRebuildsACorruptRecord covers the corrupt-cache path:
// the store treats a file it cannot parse as empty rather than
// refusing to start, so every model is new and the record is rebuilt.
func TestDiffRebuildsACorruptRecord(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, nil)
	path := writeCatalog(t, catalogProvider(srv.URL, nil))
	storePath := filepath.Join(t.TempDir(), "models-store.json")
	if err := os.WriteFile(storePath, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runMain(t, "diff", "-config", path, "-store", storePath)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "free-model:free") {
		t.Errorf("stdout = %q, want every model reported as new", stdout)
	}
	if !strings.Contains(stderr, "2 new") {
		t.Errorf("summary = %q, want the count of everything", stderr)
	}
	if got := storedIDs(t, storePath, "alpha"); !got["free-model:free"] || !got["zero-model"] {
		t.Errorf("the corrupt record was not rebuilt: %v", got)
	}
}

func TestListNewOnlyReadsTheStoreWithoutWritingIt(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, nil)
	path := writeCatalog(t, catalogProvider(srv.URL, nil))
	storePath := filepath.Join(t.TempDir(), "models-store.json")
	seedStore(t, storePath, map[string][]string{"alpha": {"zero-model"}})

	code, stdout, stderr := runMain(t, "list", "-new-only", "-config", path, "-store", storePath)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "free-model:free") {
		t.Errorf("stdout is missing the model the store had not seen:\n%s", stdout)
	}
	if strings.Contains(stdout, "zero-model") {
		t.Errorf("a known model was listed as new:\n%s", stdout)
	}
	if !strings.Contains(stderr, "1 new of 2 free models") {
		t.Errorf("summary = %q, want it to count one of two", stderr)
	}
	// -q means the same thing here as it does for a plain listing.
	if _, _, quiet := runMain(t, "list", "-new-only", "-q", "-config", path, "-store", storePath); quiet != "" {
		t.Errorf("-q still wrote a summary: %q", quiet)
	}
	// The whole point: the store is a record of what has been seen, and reading
	// it must not count as having seen anything.
	if got := storedIDs(t, storePath, "alpha"); len(got) != 1 || !got["zero-model"] {
		t.Errorf("the store changed: %v, want it left as it was found", got)
	}

	// And the same listing run again still reports the same thing.
	_, again, _ := runMain(t, "list", "-new-only", "-config", path, "-store", storePath)
	if !strings.Contains(again, "free-model:free") {
		t.Errorf("the second -new-only run consumed the novelty:\n%s", again)
	}
}

func TestListNewOnlyMarksRowsAsNew(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, nil)
	path := writeCatalog(t, catalogProvider(srv.URL, nil))
	storePath := filepath.Join(t.TempDir(), "models-store.json")
	seedStore(t, storePath, map[string][]string{"alpha": {"zero-model"}})

	code, stdout, _ := runMain(t, "list", "-new-only", "-json", "-config", path, "-store", storePath)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	var doc listDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	if len(doc.Models) != 1 || !doc.Models[0].New {
		t.Errorf("models = %+v, want one row flagged new", doc.Models)
	}
	// The per-provider counts still describe the whole catalog, not the
	// narrowed view, so the document does not contradict itself.
	if len(doc.Providers) != 1 || doc.Providers[0].Free != 2 {
		t.Errorf("providers = %+v, want the unfiltered counts", doc.Providers)
	}
}

func TestSplitModelRefKeepsSlashesInTheModel(t *testing.T) {
	cases := []struct {
		in       string
		provider string
		model    string
		ok       bool
	}{
		{"alpha/model", "alpha", "model", true},
		{"alpha/vendor/model:free", "alpha", "vendor/model:free", true},
		{"alpha/", "", "alpha/", false},
		{"/model", "", "/model", false},
		{"model", "", "model", false},
		{"", "", "", false},
	}
	for _, tc := range cases {
		provider, model, ok := splitModelRef(tc.in)
		if ok != tc.ok || provider != tc.provider || model != tc.model {
			t.Errorf("splitModelRef(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.in, provider, model, ok, tc.provider, tc.model, tc.ok)
		}
	}
}

func TestResolveTargets(t *testing.T) {
	models := []provider.Model{
		{ID: "alpha/free-model:free", Provider: "alpha", Ref: "alpha/free-model:free"},
		{ID: "alpha/paid-model", Provider: "alpha", Ref: "alpha/paid-model"},
	}
	run := &catalogRun{
		file:    &config.File{Providers: []config.Provider{{Name: "alpha"}}},
		results: []provider.FetchResult{{Provider: "alpha", Models: models}},
	}
	sel := selection{models: map[string][]provider.Model{"alpha": models}}

	t.Run("all", func(t *testing.T) {
		got, err := resolveTargets(run, sel, true, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Errorf("got %d targets, want 2", len(got))
		}
	})

	t.Run("all with a limit", func(t *testing.T) {
		got, err := resolveTargets(run, sel, true, 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Model != "alpha/free-model:free" {
			t.Errorf("got %+v, want just the first model", got)
		}
	})

	t.Run("explicit model", func(t *testing.T) {
		got, err := resolveTargets(run, sel, false, 0, []string{"alpha/vendor/model:free"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Model != "vendor/model:free" {
			t.Errorf("got %+v, want the model part only", got)
		}
	})
}

func TestProvidersCommandReportsKeySourceWithoutTheKey(t *testing.T) {
	const secret = "sk-should-never-appear-123456"
	srv := gatewayServer(t, modelsPayload, nil)
	path := writeCatalog(t, catalogProvider(srv.URL, nil))
	t.Setenv("ALPHA_KEY", secret)

	code, stdout, stderr := runMain(t, "providers", "-config", path)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	if strings.Contains(stdout+stderr, secret) {
		t.Errorf("the key reached the output:\n%s%s", stdout, stderr)
	}
	for _, want := range []string{"ALPHA_KEY", "alpha", "3"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("report is missing %q:\n%s", want, stdout)
		}
	}

	// A -key flag is reported as coming from the flag, still without its value.
	code, stdout, stderr = runMain(t, "providers", "-config", path, "-key", "alpha="+secret)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	if strings.Contains(stdout+stderr, secret) {
		t.Errorf("the flag key reached the output:\n%s%s", stdout, stderr)
	}
	if !strings.Contains(stdout, "flag") {
		t.Errorf("report should name the flag as the source:\n%s", stdout)
	}
}

func TestProvidersJSONDocument(t *testing.T) {
	srv := gatewayServer(t, modelsPayload, nil)
	path := writeCatalog(t, catalogProvider(srv.URL, nil))
	t.Setenv("ALPHA_KEY", "")

	code, stdout, stderr := runMain(t, "providers", "-json", "-config", path)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	var doc providerDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	if len(doc.Providers) != 1 {
		t.Fatalf("providers = %+v, want one", doc.Providers)
	}
	entry := doc.Providers[0]
	if entry.Name != "alpha" || entry.HasKey || entry.KeySource != "" {
		t.Errorf("entry = %+v, want alpha with no key", entry)
	}
	if entry.Models != 3 || !strings.HasSuffix(entry.Completions, "/chat/completions") {
		t.Errorf("entry = %+v, want the model count and endpoint", entry)
	}
}

func TestResolveConfigPath(t *testing.T) {
	t.Run("explicit path wins", func(t *testing.T) {
		path := writeCatalog(t, catalogProvider("https://example.invalid", nil))
		got, err := resolveConfigPath(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != path {
			t.Errorf("got %q, want the explicit path", got)
		}
	})

	t.Run("missing explicit path is an error", func(t *testing.T) {
		if _, err := resolveConfigPath(filepath.Join(t.TempDir(), "absent.json")); err == nil {
			t.Error("expected an error for a missing -config path")
		}
	})

	t.Run("the working directory is searched first", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "providers.json"), []byte(`{"providers":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)

		got, err := resolveConfigPath("")
		if err != nil {
			t.Fatal(err)
		}
		if got != "providers.json" {
			t.Errorf("got %q, want the relative name found in the working directory", got)
		}
	})

	t.Run("nothing anywhere is an error that lists where it looked", func(t *testing.T) {
		t.Chdir(t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		_, err := resolveConfigPath("")
		if err == nil {
			t.Fatal("expected an error when no catalog exists")
		}
		if !strings.Contains(err.Error(), "providers.json") {
			t.Errorf("error = %v, want it to name the file it wanted", err)
		}
	})
}
