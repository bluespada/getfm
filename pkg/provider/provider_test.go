package provider

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bluespada/getfm/pkg/config"
)

func TestParseOpenRouterShape(t *testing.T) {
	p := &config.Provider{
		Name: "openrouter",
		Mapping: config.Mapping{
			List: "data", ID: "id", Name: "name", Context: "context_length",
			PromptPrice: "pricing.prompt", CompletionPrice: "pricing.completion",
		},
	}
	body := `{"data":[
		{"id":"qwen/qwen3:free","name":"Qwen 3","context_length":262144,
		 "pricing":{"prompt":"0","completion":"0"}},
		{"id":"paid/model","name":"Paid","context_length":8192,
		 "pricing":{"prompt":"0.0000003","completion":"0.0000012"}}
	]}`

	models, err := parseJSON(t, p, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
	free := models[0]
	if free.ID != "qwen/qwen3:free" || free.Context != 262144 {
		t.Errorf("unexpected model: %+v", free)
	}
	if !free.Priced || free.PromptPrice != 0 || free.CompletionPrice != 0 {
		t.Errorf("string prices were not parsed: %+v", free)
	}
	if models[1].PromptPrice != 0.0000003 {
		t.Errorf("non-zero price parsed wrong: %+v", models[1])
	}
}

func TestParseGeminiShapeWithStripPrefix(t *testing.T) {
	p := &config.Provider{
		Name: "gemini",
		Mapping: config.Mapping{
			List: "models", ID: "name", StripPrefix: "models/",
			Name: "displayName", Context: "inputTokenLimit",
		},
	}
	body := `{"models":[{"name":"models/gemini-2.0-flash","displayName":"Gemini 2.0 Flash","inputTokenLimit":1048576}]}`

	models, err := parseJSON(t, p, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	m := models[0]
	if m.ID != "models/gemini-2.0-flash" {
		t.Errorf("ID should stay as reported, got %q", m.ID)
	}
	// Ref is what goes in the request path, where the prefix must be gone.
	if m.Ref != "gemini-2.0-flash" {
		t.Errorf("Ref = %q, want the prefix stripped", m.Ref)
	}
	if m.Name != "Gemini 2.0 Flash" || m.Context != 1048576 {
		t.Errorf("unexpected model: %+v", m)
	}
	// Gemini publishes no pricing, so Priced must stay false.
	if m.Priced {
		t.Error("gemini should not be marked as priced")
	}
}

func TestParseSkipsEntriesWithoutID(t *testing.T) {
	p := &config.Provider{Name: "x", Mapping: config.Mapping{List: "data", ID: "id"}}
	models, err := parseJSON(t, p, `{"data":[{"name":"no id here"},{"id":"good"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "good" {
		t.Errorf("expected only the identified model, got %+v", models)
	}
}

func TestParseHalfPublishedPricingIsNotPriced(t *testing.T) {
	// Only an input price is mapped, so nothing about the output side is known.
	// Treating the unpublished half as zero would report a charged model as free.
	p := &config.Provider{
		Name:    "half",
		Mapping: config.Mapping{List: "data", ID: "id", PromptPrice: "pricing.prompt"},
	}
	models, err := parseJSON(t, p, `{"data":[{"id":"m","pricing":{"prompt":"0"}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if models[0].Priced {
		t.Errorf("half-published pricing was reported as priced: %+v", models[0])
	}
	if models[0].PromptPrice != 0 || models[0].CompletionPrice != 0 {
		t.Errorf("prices should stay unset: %+v", models[0])
	}
}

func TestParseMissingListIsAnError(t *testing.T) {
	p := &config.Provider{Name: "x", Mapping: config.Mapping{List: "data", ID: "id"}}
	if _, err := parseJSON(t, p, `{"models":[]}`); err == nil {
		t.Fatal("expected an error when the configured array is absent")
	}
}

func TestExcerptRedactsSecrets(t *testing.T) {
	body := []byte(`{"error":"bad key sk-abc123def456ghi789 provided, also gsk_0123456789abcdef"}`)
	got := Excerpt(body, 500, "sk-abc123def456ghi789")
	if strings.Contains(got, "sk-abc123def456ghi789") {
		t.Errorf("literal secret survived redaction: %s", got)
	}
	if strings.Contains(got, "gsk_0123456789abcdef") {
		t.Errorf("pattern secret survived redaction: %s", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Errorf("expected redaction marker, got: %s", got)
	}
}

func TestExcerptTruncatesAndFlattens(t *testing.T) {
	got := Excerpt([]byte("  a\n\tb   c  "), 500)
	if got != "a b c" {
		t.Errorf("whitespace was not flattened: %q", got)
	}
	long := Excerpt([]byte("0123456789"), 4)
	if long != "0123..." {
		t.Errorf("truncation wrong: %q", long)
	}
}

// TestExcerptNeverSplitsACharacter keeps a truncated error body or response
// from reaching the terminal as invalid UTF-8, which a byte slice would do.
func TestExcerptNeverSplitsACharacter(t *testing.T) {
	body := []byte(strings.Repeat("héllo wörld ", 8))
	for max := 1; max < len(body); max++ {
		got := Excerpt(body, max)
		if !utf8.ValidString(got) {
			t.Fatalf("max %d produced invalid UTF-8: %q", max, got)
		}
	}
}

func parseJSON(t *testing.T, p *config.Provider, body string) ([]Model, error) {
	t.Helper()
	var root any
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		t.Fatal(err)
	}
	return Parse(p, root)
}
