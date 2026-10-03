package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bluespada/getfm/pkg/config"
	"github.com/bluespada/getfm/pkg/provider"
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
