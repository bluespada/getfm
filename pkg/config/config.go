// Package config loads and validates the provider catalog that drives getfm.
//
// The catalog lives in providers.json. Everything the tool needs to talk to an
// upstream API - the models endpoint, how to find the interesting fields in its
// response, and how to shape a completion request - is declared there, so adding
// a provider is a JSON edit rather than a code change.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// File is the on-disk shape of providers.json.
type File struct {
	Providers []Provider `json:"providers"`
}

// Provider describes one upstream API.
type Provider struct {
	// Name is the short identifier used as `provider/model` on the command line.
	Name string `json:"name"`
	// Label is the human readable name shown in output.
	Label string `json:"label"`
	// BaseURL is the scheme and host, without a trailing slash.
	BaseURL string `json:"base_url"`
	// ModelsPath is appended to BaseURL to list models.
	ModelsPath string `json:"models_path"`
	// EnvKeys are inspected in order; the first non-empty one supplies the key.
	EnvKeys []string `json:"env_keys"`
	// Headers are sent with the models request. Most models endpoints are
	// public, so this is empty for most providers; one that gates its catalog
	// behind auth declares what to send here, for example
	// {"Authorization": "Bearer {{.Key}}"}.
	Headers map[string]string `json:"headers"`
	// compiledHeaders holds Headers parsed into templates at load time.
	compiledHeaders []headerTemplate
	// FreeAlways marks every model from this provider as free. Local endpoints
	// that cost nothing to run use this instead of declaring pricing.
	FreeAlways bool `json:"free_always"`
	// FreeIDs are model ids that are free by declaration. Providers that publish
	// no pricing cannot be detected by price, so their free models are listed
	// here instead. Entries accept * and ? wildcards.
	FreeIDs []string `json:"free_ids"`
	// compiledFreeIDs holds FreeIDs compiled into matchers at load time.
	compiledFreeIDs []func(string) bool
	// Mapping locates the fields getfm cares about in the models response.
	Mapping Mapping `json:"mapping"`
	// Completions describes how to send a minimal request. A provider without
	// it can be listed but not probed.
	Completions *Probe `json:"completions"`
}

// Mapping locates the interesting fields in a provider's models response.
//
// List is a dotted path to the array of model objects; every other path is
// resolved relative to each element of that array. Empty paths mean the field
// is absent, which is how providers that publish no pricing are described.
type Mapping struct {
	List            string `json:"list"`
	ID              string `json:"id"`
	Name            string `json:"name"`
	Context         string `json:"context"`
	PromptPrice     string `json:"prompt_price"`
	CompletionPrice string `json:"completion_price"`
	// StripPrefix is removed from an id before it is used in a completion
	// request. Gemini reports `models/gemini-2.0-flash` but the request path
	// wants only the part after the slash.
	StripPrefix string `json:"strip_prefix"`
}

// Probe describes how to send a minimal completion request.
type Probe struct {
	// Path is appended to BaseURL. It may contain {{.Model}}.
	Path string `json:"path"`
	// Method defaults to POST.
	Method string `json:"method"`
	// AuthHeader names the header the key is sent in. Empty sends no auth at
	// all, which is what local servers want.
	AuthHeader string `json:"auth_header"`
	// AuthPrefix is prepended to the key, conventionally "Bearer ".
	AuthPrefix string `json:"auth_prefix"`
	// Body is a JSON template rendered with {{.Model}} and {{.Prompt}}.
	Body string `json:"body"`
	// Usage is a dotted path to a token count in the response, when reported.
	Usage string `json:"usage"`
}

// Load reads and validates a catalog from disk.
func Load(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f File
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(f.Providers) == 0 {
		return nil, fmt.Errorf("%s defines no providers", path)
	}
	seen := make(map[string]bool, len(f.Providers))
	for i := range f.Providers {
		p := &f.Providers[i]
		if err := p.normalize(); err != nil {
			return nil, fmt.Errorf("provider %d in %s: %w", i+1, path, err)
		}
		if seen[p.Name] {
			return nil, fmt.Errorf("duplicate provider name %q", p.Name)
		}
		seen[p.Name] = true
	}
	return &f, nil
}

// CompileFreeIDs parses the declared free ids into matchers.
func (p *Provider) CompileFreeIDs() error {
	matchers := make([]func(string) bool, 0, len(p.FreeIDs))
	for _, id := range p.FreeIDs {
		m, err := compileFreeID(id)
		if err != nil {
			return fmt.Errorf("free_ids: %w", err)
		}
		matchers = append(matchers, m)
	}
	p.compiledFreeIDs = matchers
	return nil
}

// DeclaredFree returns matchers for the ids the catalog declares free. A model
// matching any of them is free regardless of what the provider priced it at.
func (p Provider) DeclaredFree() []func(string) bool { return p.compiledFreeIDs }

// normalize fills in defaults and rejects providers getfm cannot use.
func (p *Provider) normalize() error {
	p.Name = strings.TrimSpace(p.Name)
	p.BaseURL = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")

	if p.Name == "" {
		return fmt.Errorf("missing name")
	}
	if p.BaseURL == "" {
		return fmt.Errorf("missing base_url")
	}
	if !strings.HasPrefix(p.BaseURL, "http://") && !strings.HasPrefix(p.BaseURL, "https://") {
		return fmt.Errorf("base_url must start with http:// or https://")
	}
	if p.ModelsPath == "" {
		p.ModelsPath = "/v1/models"
	}
	if p.Mapping.ID == "" {
		return fmt.Errorf("missing mapping.id")
	}
	if p.Label == "" {
		p.Label = p.Name
	}
	if err := p.CompileFreeIDs(); err != nil {
		return fmt.Errorf("provider %s: %w", p.Name, err)
	}
	if err := p.CompileHeaders(); err != nil {
		return fmt.Errorf("provider %s: %w", p.Name, err)
	}

	if c := p.Completions; c != nil {
		if c.Path == "" {
			return fmt.Errorf("completions.path is required")
		}
		if c.Method == "" {
			c.Method = "POST"
		}
		c.Method = strings.ToUpper(c.Method)
		if c.Body == "" {
			return fmt.Errorf("completions.body is required")
		}
	}
	return nil
}

// ModelsURL is the fully qualified endpoint that lists models.
func (p *Provider) ModelsURL() string {
	return p.BaseURL + p.ModelsPath
}

// Key returns the first non-empty key from overrides, then from the declared
// environment variables. It returns "" when the provider needs none.
func (p *Provider) Key(overrides map[string]string, environ func(string) string) string {
	if v := overrides[p.Name]; v != "" {
		return v
	}
	for _, name := range p.EnvKeys {
		if environ == nil {
			break
		}
		if v := environ(name); v != "" {
			return v
		}
	}
	return ""
}

// KeySource names where a key came from, for reporting. It never returns any
// part of the key itself.
func (p *Provider) KeySource(overrides map[string]string, environ func(string) string) string {
	if overrides[p.Name] != "" {
		return "flag"
	}
	for _, name := range p.EnvKeys {
		if environ == nil {
			break
		}
		if environ(name) != "" {
			return name
		}
	}
	return ""
}

// Get returns the provider with the given name.
func (f *File) Get(name string) (Provider, bool) {
	for _, p := range f.Providers {
		if p.Name == name {
			return p, true
		}
	}
	return Provider{}, false
}
