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
	"net"
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
	// Reason is the class of failure, captured only where it cannot be
	// recovered later: a transport error is typed information that Detail
	// flattens away. Classify fills in everything else.
	Reason Class `json:"reason,omitempty"`

	// Request is what was sent and Response what came back, both redacted.
	Request  Prepared  `json:"request,omitempty"`
	Response *Exchange `json:"response,omitempty"`
}

// Class is a coarse category for a probe outcome.
//
// A status code and a captured body say what happened; a class says what it
// means for the person running the probe, which is the question the raw numbers
// leave open: is the key wrong, is the allowance spent, or has the model been
// withdrawn?
type Class string

const (
	ClassOK   Class = "ok"
	ClassAuth Class = "auth"
	// ClassQuota is an authenticated request that ran out of allowance.
	ClassQuota Class = "quota"
	// ClassRate is throttling, which is worth retrying later.
	ClassRate Class = "rate"
	// ClassGone means the provider no longer offers this model.
	ClassGone Class = "gone"
	// ClassRequest is any other 4xx: the request itself was wrong, a body
	// the endpoint would not accept or a model it will not serve under
	// that name.
	ClassRequest Class = "request"
	// ClassServer is a failure on the provider's side rather than ours.
	ClassServer Class = "server"
	// ClassNetwork never reached the provider at all.
	ClassNetwork Class = "network"
	ClassTimeout Class = "timeout"
	// ClassConfig is a malformed catalog entry: a body that is not JSON, a
	// host that does not resolve, or an endpoint that bounces forever.
	ClassConfig  Class = "config"
	ClassUnknown Class = "unknown"
)

// Classify names the class of a result.
//
// A Reason captured by Send wins when present, since that is the one judgement
// made while the typed error was still in hand; everything else is derived from
// the status and the captured body.
func Classify(res Result) Class {
	if res.OK {
		return ClassOK
	}
	if res.Reason != "" {
		return res.Reason
	}
	// No status means the request never completed, so the body is empty and
	// only the detail can be read.
	if res.Status == 0 {
		if mentionsAny(res.Detail, "timeout", "timed out", "deadline exceeded") {
			return ClassTimeout
		}
		return ClassNetwork
	}
	return classForStatus(res.Status, res.Detail)
}

// classForStatus maps an HTTP response to a class. The ambiguous codes are
// resolved by reading the body, which is the only place the distinction is made.
func classForStatus(status int, detail string) Class {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return ClassAuth
	case status == http.StatusPaymentRequired:
		// Payment required is unambiguous: it is the provider asking to be paid.
		return ClassQuota
	case status == http.StatusTooManyRequests:
		// A 429 is either throttling or an exhausted allowance, and the two call
		// for opposite responses: come back later, or top up.
		if mentionsAny(detail, "quota", "billing", "credit", "balance", "insufficient") {
			return ClassQuota
		}
		return ClassRate
	case status == http.StatusNotFound, status == http.StatusGone:
		// A 404 is read as a withdrawn model rather than a misconfigured
		// path: the probe targets one model, so a missing endpoint and a
		// missing model look the same here.
		return ClassGone
	case status >= 500:
		return ClassServer
	case status >= 300 && status < 400:
		// The client follows redirects, so a 3xx only surfaces when it gave
		// up: the endpoint is configured to bounce forever.
		return ClassConfig
	default:
		// Any other 4xx is the request itself.
		return ClassRequest
	}
}

// classifyTransport reads the typed error a failed request produced.
func classifyTransport(err error) Class {
	// A name that does not resolve is a catalog problem, and it is
	// checked before the timeout cases: a resolver that times out is
	// still a host that never resolved.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ClassConfig
	}
	// Timeout is checked before anything else because a timeout is never the
	// caller's fault, whereas the classes below are.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ClassTimeout
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ClassTimeout
	}
	if mentionsAny(err.Error(), "timeout", "timed out", "deadline exceeded") {
		return ClassTimeout
	}
	return ClassNetwork
}

// mentionsAny reports a case-insensitive hit for any of the needles. It is
// deliberately loose: these strings are read out of bodies meant for humans and
// for other clients, and a miss only costs precision, never correctness of the
// status-based fallback.
func mentionsAny(haystack string, needles ...string) bool {
	lower := strings.ToLower(haystack)
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
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
		// The catalog itself is at fault, not the endpoint. The text is
		// scrubbed like a body, because a rendered path can echo a
		// configured credential.
		res.Reason = ClassConfig
		res.Detail = provider.Excerpt([]byte(err.Error()), 140, req.Key)
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
		res.Reason = ClassConfig
		res.Detail = provider.Excerpt([]byte(fmt.Sprintf("build request: %v", err)), 140, req.Key)
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
		// Classified here rather than on the detail string, because a dial
		// error is the one failure whose type is lost the moment it is
		// flattened for display.
		res.Reason = classifyTransport(err)
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
