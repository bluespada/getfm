package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCatalog(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadValidCatalog(t *testing.T) {
	path := writeCatalog(t, `{"providers":[{
		"name":"local","base_url":"http://localhost:1234/","models_path":"/models",
		"env_keys":["LOCAL_KEY"],"free_always":true,
		"mapping":{"list":"models","id":"name"},
		"completions":{"path":"/gen","body":"{}"}
	}]}`)

	file, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := file.Get("local")
	if !ok {
		t.Fatal("provider not found by name")
	}
	// The trailing slash on base_url is stripped so path joining stays correct.
	if p.BaseURL != "http://localhost:1234" {
		t.Errorf("base_url = %q, want the trailing slash removed", p.BaseURL)
	}
	if p.ModelsURL() != "http://localhost:1234/models" {
		t.Errorf("models URL = %q", p.ModelsURL())
	}
	if p.Label != "local" {
		t.Errorf("label should default to the name, got %q", p.Label)
	}
	if p.Completions.Method != "POST" {
		t.Errorf("method should default to POST, got %q", p.Completions.Method)
	}
}

func TestLoadRejectsBadCatalogs(t *testing.T) {
	cases := map[string]string{
		"empty":            `{"providers":[]}`,
		"missing name":     `{"providers":[{"base_url":"http://x","mapping":{"id":"id"}}]}`,
		"bad scheme":       `{"providers":[{"name":"a","base_url":"ftp://x","mapping":{"id":"id"}}]}`,
		"missing base url": `{"providers":[{"name":"a","mapping":{"id":"id"}}]}`,
		"missing id map":   `{"providers":[{"name":"a","base_url":"http://x"}]}`,
		"unknown field":    `{"providers":[{"name":"a","base_url":"http://x","mapping":{"id":"id"},"typo":1}]}`,
		"duplicate name": `{"providers":[
			{"name":"a","base_url":"http://x","mapping":{"id":"id"}},
			{"name":"a","base_url":"http://y","mapping":{"id":"id"}}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeCatalog(t, body)); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestKeyPrefersFlagThenEnv(t *testing.T) {
	p := Provider{Name: "a", EnvKeys: []string{"FIRST", "SECOND"}}
	env := map[string]string{"FIRST": "", "SECOND": "from-env"}

	if got := p.Key(nil, lookup(env)); got != "from-env" {
		t.Errorf("env fallback = %q", got)
	}
	flags := map[string]string{"a": "from-flag"}
	if got := p.Key(flags, lookup(env)); got != "from-flag" {
		t.Errorf("flag should win, got %q", got)
	}
	if got := p.Key(nil, lookup(map[string]string{})); got != "" {
		t.Errorf("expected no key, got %q", got)
	}
}

func TestKeySourceNamesTheVariableNotTheValue(t *testing.T) {
	p := Provider{Name: "a", EnvKeys: []string{"SOME_KEY"}}
	env := map[string]string{"SOME_KEY": "super-secret-value"}
	src := p.KeySource(nil, lookup(env))
	if src != "SOME_KEY" {
		t.Errorf("source = %q, want the variable name", src)
	}
	if strings.Contains(src, "super-secret-value") {
		t.Error("key source leaked the key")
	}
}

func TestLookupFloatAcceptsStringsAndNumbers(t *testing.T) {
	v := map[string]any{
		"str": "0.5",
		"num": float64(2),
		"bad": "not-a-number",
	}
	if got, ok := LookupFloat(v, "str"); !ok || got != 0.5 {
		t.Errorf("string price: got %v ok=%v", got, ok)
	}
	if got, ok := LookupFloat(v, "num"); !ok || got != 2 {
		t.Errorf("number price: got %v ok=%v", got, ok)
	}
	if _, ok := LookupFloat(v, "bad"); ok {
		t.Error("unparsable price should report missing")
	}
	if _, ok := LookupFloat(v, ""); ok {
		t.Error("an empty path must always report missing")
	}
}

func TestLookupIntAcceptsStringifiedNumbers(t *testing.T) {
	v := map[string]any{"n": "1048576"}
	if got := LookupInt(v, "n"); got != 1048576 {
		t.Errorf("got %d", got)
	}
	if got := LookupInt(v, "missing"); got != 0 {
		t.Errorf("missing key should be 0, got %d", got)
	}
}

func lookup(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}
