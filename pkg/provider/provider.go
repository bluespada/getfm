// Package provider fetches model catalogues from the configured providers.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/bluespada/getfm/pkg/config"
)

// Model is one entry from a provider's models endpoint.
type Model struct {
	Provider        string  `json:"provider"`
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Context         int64   `json:"context_length,omitempty"`
	PromptPrice     float64 `json:"prompt_price"`
	CompletionPrice float64 `json:"completion_price"`
	// Priced reports whether the provider actually published pricing. A model
	// with no pricing is not the same as a model priced at zero.
	Priced bool `json:"priced"`
	// Reason explains why the model was judged free. Empty when it was not.
	Reason string `json:"free_reason,omitempty"`
	// Ref is the provider-specific identifier to send in a completion request,
	// after any configured prefix was stripped.
	Ref string `json:"-"`
	// Raw is the provider's own record for this model, kept verbatim so the
	// browser can show the full card rather than a fixed subset of fields.
	Raw map[string]any `json:"-"`
}

// Catalog holds the models found for a set of providers.
type Catalog map[string][]Model

// FetchResult records the outcome of querying a single provider, so one
// unreachable endpoint never hides the results from the others.
type FetchResult struct {
	Provider string
	Models   []Model
	Err      error
	Elapsed  int64 // milliseconds
}

// Options controls how catalogs are fetched.
type Options struct {
	// Concurrency bounds simultaneous in-flight provider requests.
	Concurrency int
	// MaxBytes caps a models response. Catalogs run into the low megabytes.
	MaxBytes int64
	// Key supplies the API key for a provider, or "" when it needs none.
	Key func(config.Provider) string
}

// Fetch queries every provider concurrently and returns results in catalog
// order. The returned slice always has one entry per provider.
func Fetch(ctx context.Context, client *http.Client, file *config.File, opts Options) []FetchResult {
	if opts.Concurrency < 1 {
		opts.Concurrency = 4
	}
	if opts.MaxBytes < 1 {
		opts.MaxBytes = 16 << 20
	}

	results := make([]FetchResult, len(file.Providers))
	sem := make(chan struct{}, opts.Concurrency)
	var wg sync.WaitGroup

	for i := range file.Providers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := file.Providers[i]

			sem <- struct{}{}
			defer func() { <-sem }()

			start := nowMillis()
			var key string
			if opts.Key != nil {
				key = opts.Key(p)
			}
			models, err := List(ctx, client, &p, key, opts)
			results[i] = FetchResult{
				Provider: p.Name,
				Models:   models,
				Err:      err,
				Elapsed:  nowMillis() - start,
			}
		}(i)
	}
	wg.Wait()
	return results
}

// List fetches and decodes one provider's models response.
func List(ctx context.Context, client *http.Client, p *config.Provider, key string, opts Options) ([]Model, error) {
	maxBytes := opts.MaxBytes
	if maxBytes < 1 {
		maxBytes = 16 << 20
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.ModelsURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	// A key on its own is not enough to say what to send, so the catalog says
	// it: most models endpoints are public and want no credential at all,
	// while the ones that gate their catalog behind auth declare the headers
	// to send. Those win over the defaults set above.
	if key != "" {
		headers, err := p.ModelsHeaders(key)
		if err != nil {
			return nil, err
		}
		for name, value := range headers {
			req.Header.Set(name, value)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, Excerpt(body, 160, key))
	}

	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return Parse(p, root)
}

// Parse turns a decoded models response into Model values using the provider's
// configured field mapping.
func Parse(p *config.Provider, root any) ([]Model, error) {
	listPath := p.Mapping.List
	if listPath == "" {
		listPath = "data"
	}
	raw, ok := config.Lookup(root, listPath)
	if !ok {
		return nil, fmt.Errorf("no %q array in response", listPath)
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%q is not an array", listPath)
	}

	models := make([]Model, 0, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := config.LookupString(obj, p.Mapping.ID)
		if id == "" {
			continue
		}
		m := Model{
			Provider: p.Name,
			ID:       id,
			Name:     config.LookupString(obj, p.Mapping.Name),
			Context:  config.LookupInt(obj, p.Mapping.Context),
			Ref:      id,
			Raw:      obj,
		}
		if m.Name == "" {
			m.Name = id
		}
		// Both prices have to be published before the pricing can be judged at
		// all: a model with only an input price and no output price is not "free
		// to run", it is incompletely described, and the zero price rule must
		// not read the missing half as zero.
		prompt, promptPriced := config.LookupFloat(obj, p.Mapping.PromptPrice)
		completion, completionPriced := config.LookupFloat(obj, p.Mapping.CompletionPrice)
		if promptPriced && completionPriced {
			m.PromptPrice, m.CompletionPrice, m.Priced = prompt, completion, true
		}
		if pre := p.Mapping.StripPrefix; pre != "" {
			m.Ref = strings.TrimPrefix(id, pre)
		}
		models = append(models, m)
	}
	return models, nil
}

// secretPatterns match the key formats the common providers issue, so an
// upstream error body that echoes a credential does not reach the terminal.
// The character classes include "*" because providers often echo a partially
// masked key such as "sk-abc123****7890" in their error messages.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_\-*]{12,}`),
	regexp.MustCompile(`gsk_[A-Za-z0-9_\-*]{12,}`),
	regexp.MustCompile(`AIza[A-Za-z0-9_\-*]{20,}`),
	regexp.MustCompile(`xai-[A-Za-z0-9_\-*]{12,}`),
	regexp.MustCompile(`Bearer\s+[A-Za-z0-9._\-*]{12,}`),
}

const redacted = "[redacted]"

// Excerpt trims a response body down to something safe and readable to print in
// an error message: single line, length capped, with any known key removed.
//
// secrets holds the values this process actually sent. They are replaced
// literally first, because that is exact; the pattern pass then catches masked
// or partial forms the provider produced on its own.
func Excerpt(body []byte, max int, secrets ...string) string {
	s := strings.TrimSpace(string(body))
	for _, secret := range secrets {
		if len(secret) >= 8 {
			s = strings.ReplaceAll(s, secret, redacted)
		}
	}
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, redacted)
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = clip(s, max) + "..."
	}
	return s
}

// clip shortens s to at most max bytes without splitting a multi-byte
// character, which a plain byte slice would.
func clip(s string, max int) string {
	if max >= len(s) {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}
