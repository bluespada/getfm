package config

import (
	"fmt"
	"sort"
	"strings"
	"text/template"
)

// headerTemplate is one declared header with its value already parsed.
type headerTemplate struct {
	name string
	tmpl *template.Template
}

// CompileHeaders parses the declared header values and checks the names. It is
// the same bargain as CompileFreeIDs: a catalog mistake should surface in
// `getfm providers`, not on the first fetch against a live endpoint.
func (p *Provider) CompileHeaders() error {
	if len(p.Headers) == 0 {
		p.compiledHeaders = nil
		return nil
	}

	names := make([]string, 0, len(p.Headers))
	for name := range p.Headers {
		names = append(names, name)
	}
	// A JSON object has no order, so sort to keep the first error reported
	// stable between runs.
	sort.Strings(names)

	compiled := make([]headerTemplate, 0, len(names))
	for _, name := range names {
		if !validHeaderName(name) {
			return fmt.Errorf("headers: %q is not a valid header name", name)
		}
		tmpl, err := template.New(name).Parse(p.Headers[name])
		if err != nil {
			return fmt.Errorf("headers: %s: %w", name, err)
		}
		compiled = append(compiled, headerTemplate{name: name, tmpl: tmpl})
	}
	p.compiledHeaders = compiled
	return nil
}

// ModelsHeaders renders the headers to send with the models request. Values are
// templates and {{.Key}} expands to the provider's resolved API key, which is
// what lets a catalog say "Bearer {{.Key}}" without the key ever being written
// into the file.
//
// Without a key there is nothing to inject and an empty credential is worse
// than none, so a provider that resolves no key sends no declared headers.
func (p Provider) ModelsHeaders(key string) (map[string]string, error) {
	if key == "" || len(p.compiledHeaders) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(p.compiledHeaders))
	for _, h := range p.compiledHeaders {
		var buf strings.Builder
		data := struct{ Key string }{Key: key}
		if err := h.tmpl.Execute(&buf, data); err != nil {
			return nil, fmt.Errorf("headers: %s: %w", h.name, err)
		}
		value := buf.String()
		// A value carrying a line break would let a key rewrite the request
		// that carries it, so refuse it here rather than deep inside net/http.
		if strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("headers: %s rendered a value containing a line break", h.name)
		}
		out[h.name] = value
	}
	return out, nil
}

// validHeaderName reports whether name is an RFC 7230 token, which is the only
// thing net/http accepts.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}
