// Package probe sends a minimal completion request to confirm that an endpoint
// and its credentials actually work.
//
// The request is deliberately as small as a provider allows: one user message,
// one output token. It exists to prove reachability, authentication and
// generation, not to measure quality or throughput.
//
// Every probe keeps the request it sent and the response it got back, redacted
// and size-capped, so the browser can show exactly what crossed the wire.
package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/template"
	"time"

	"github.com/bluespada/getfm/pkg/config"
	"github.com/bluespada/getfm/pkg/provider"
)

// DefaultPrompt is the shortest instruction that still elicits a token.
const DefaultPrompt = "hi"

// maxCapturedBody bounds what is retained for display. A one-token completion
// answers in a few hundred bytes; anything far larger is an error page.
const maxCapturedBody = 16 << 10

// Prepared is a fully rendered request before it is sent. Preparing without
// sending is how the browser can show what getfm would transmit, even for a
// model that has never been probed.
type Prepared struct {
	Method string      `json:"method"`
	URL    string      `json:"url"`
	Header http.Header `json:"-"`
	Body   string      `json:"body,omitempty"`
}

// Safe returns a copy that is safe to display. The credential is replaced with a
// marker rather than dropped, so it stays visible that auth is being sent.
func (p Prepared) Safe(authHeader, authPrefix string) Prepared {
	out := p
	out.Header = p.Header.Clone()
	if out.Header == nil {
		out.Header = http.Header{}
	}
	if authHeader != "" {
		out.Header.Set(authHeader, authPrefix+"[redacted]")
	}
	return out
}

// Exchange is the response half of a probe.
type Exchange struct {
	Header http.Header `json:"-"`
	Body   string      `json:"body,omitempty"`
	// Truncated reports that Body was cut to stay within the capture limit.
	Truncated bool `json:"truncated,omitempty"`
}

// Result is the outcome of one probe.
type Result struct {
	Provider string        `json:"provider"`
	Model    string        `json:"model"`
	OK       bool          `json:"ok"`
	Status   int           `json:"status"`
	Latency  time.Duration `json:"-"`
	Tokens   int64         `json:"tokens,omitempty"`
	// Detail carries the reason a probe failed, trimmed to stay readable.
	Detail string `json:"detail,omitempty"`

	// Request is what was sent and Response what came back, both redacted.
	Request  Prepared  `json:"request,omitempty"`
	Response *Exchange `json:"response,omitempty"`
}

// Request describes a single probe.
type Request struct {
	Provider config.Provider
	// Model is the provider's own identifier, shown in output.
	Model string
	// Ref is the identifier to send, after any configured prefix was stripped.
	Ref string
	// Prompt defaults to DefaultPrompt.
	Prompt string
	Key    string
}

// Prepare renders a probe into the exact request that would be sent. It never
// attaches the credential, so what it returns is safe to keep and display.
func Prepare(req Request) (Prepared, error) {
	c := req.Provider.Completions
	if c == nil {
		return Prepared{}, errors.New("provider declares no completions endpoint")
	}

	path, err := render(c.Path, req.Ref, req.Prompt)
	if err != nil {
		return Prepared{}, fmt.Errorf("bad completions.path: %w", err)
	}
	// A path template can inject characters needing escaping, so rebuild the
	// URL rather than concatenating strings.
	u, err := url.Parse(req.Provider.BaseURL + path)
	if err != nil {
		return Prepared{}, fmt.Errorf("bad endpoint URL: %w", err)
	}

	body, err := render(c.Body, req.Ref, req.Prompt)
	if err != nil {
		return Prepared{}, fmt.Errorf("bad completions.body: %w", err)
	}
	if !json.Valid([]byte(body)) {
		return Prepared{}, errors.New("completions.body did not render to valid JSON")
	}

	method := c.Method
	if method == "" {
		method = http.MethodPost
	}

	header := http.Header{}
	header.Set("Content-Type", "application/json")
	header.Set("Accept", "application/json")
	header.Set("User-Agent", "getfm")

	return Prepared{Method: method, URL: u.String(), Header: header, Body: body}, nil
}

// Send performs one probe. It never returns an error for an upstream failure:
// a 401 or a 500 is a result to report, not a crash.
func Send(ctx context.Context, client *http.Client, req Request) Result {
	c := req.Provider.Completions
	res := Result{Provider: req.Provider.Name, Model: req.Model}

	prep, err := Prepare(req)
	if err != nil {
		res.Detail = err.Error()
		return res
	}

	// Prepare has already rejected a provider without a completions endpoint,
	// so c is non-nil from here on.
	authHeader, authPrefix := c.AuthHeader, c.AuthPrefix
	// The stored copy records that a credential is attached, without ever
	// holding the value.
	res.Request = prep.Safe(authHeader, authPrefix)

	httpReq, err := http.NewRequestWithContext(ctx, prep.Method, prep.URL, bytes.NewReader([]byte(prep.Body)))
	if err != nil {
		res.Detail = fmt.Sprintf("build request: %v", err)
		return res
	}
	httpReq.Header = prep.Header.Clone()
	// Attached last, and only to the outgoing request, so the key can never
	// reach a stored result, a log or an error message.
	if req.Key != "" && authHeader != "" {
		httpReq.Header.Set(authHeader, authPrefix+req.Key)
	}

	start := time.Now()
	resp, err := client.Do(httpReq)
	res.Latency = time.Since(start)
	if err != nil {
		// A dial error is shown the same way a body is: flattened, shortened
		// and scrubbed, since a URL or a proxy error can carry a credential.
		res.Detail = provider.Excerpt([]byte(err.Error()), 140, req.Key)
		return res
	}
	defer resp.Body.Close()

	res.Status = resp.StatusCode
	res.OK = resp.StatusCode >= 200 && resp.StatusCode < 300

	payload, _ := io.ReadAll(io.LimitReader(resp.Body, maxCapturedBody+1))
	// The flag has to be captured before slicing, or the slice hides it.
	truncated := len(payload) > maxCapturedBody
	if truncated {
		payload = payload[:maxCapturedBody]
	}
	res.Response = &Exchange{
		Header:    resp.Header.Clone(),
		Body:      provider.Excerpt(payload, maxCapturedBody, req.Key),
		Truncated: truncated,
	}

	if !res.OK {
		res.Detail = provider.Excerpt(payload, 200, req.Key)
		if res.Detail == "" {
			res.Detail = http.StatusText(resp.StatusCode)
		}
		return res
	}
	if c.Usage != "" && len(payload) > 0 {
		var root any
		if json.Unmarshal(payload, &root) == nil {
			res.Tokens = config.LookupInt(root, c.Usage)
		}
	}
	return res
}

// SendAll probes several models with a bounded number in flight at once. Free
// tiers are usually rate limited, so the default concurrency is low on purpose;
// a 429 is reported per model rather than retried.
func SendAll(ctx context.Context, client *http.Client, targets []Request, concurrency int) []Result {
	if concurrency < 1 {
		concurrency = 2
	}
	results := make([]Result, len(targets))
	sem := make(chan struct{}, concurrency)
	done := make(chan struct{}, len(targets))

	for i := range targets {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = Send(ctx, client, targets[i])
		}(i)
	}
	for range targets {
		<-done
	}
	return results
}

// render expands the two placeholders a catalog may use. Values are escaped for
// use inside a JSON string literal, so a prompt containing quotes cannot break
// the request body.
func render(text, model, prompt string) (string, error) {
	if !strings.Contains(text, "{{") {
		return text, nil
	}
	t, err := template.New("probe").Parse(text)
	if err != nil {
		return "", err
	}
	data := struct {
		Model  string
		Prompt string
	}{Model: model, Prompt: prompt}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	out := buf.String()
	// Re-escape only where the template embedded the value inside quotes, which
	// is how every catalog in practice writes a JSON body.
	if strings.Contains(text, `"{{.Model}}"`) {
		out = strings.ReplaceAll(out, `"`+model+`"`, `"`+jsonEscape(model)+`"`)
	}
	if strings.Contains(text, `"{{.Prompt}}"`) {
		out = strings.ReplaceAll(out, `"`+prompt+`"`, `"`+jsonEscape(prompt)+`"`)
	}
	return out, nil
}

func jsonEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return s
	}
	return string(b[1 : len(b)-1])
}
