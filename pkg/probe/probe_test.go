package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bluespada/getfm/pkg/config"
)

func testProvider(base string) config.Provider {
	return config.Provider{
		Name:    "test",
		BaseURL: base,
		Mapping: config.Mapping{ID: "id"},
		Completions: &config.Probe{
			Path:       "/v1/chat/completions",
			Method:     "POST",
			AuthHeader: "Authorization",
			AuthPrefix: "Bearer ",
			Body:       `{"model":"{{.Model}}","messages":[{"role":"user","content":"{{.Prompt}}"}],"max_tokens":1}`,
			Usage:      "usage.total_tokens",
		},
	}
}

func TestSendSuccess(t *testing.T) {
	var gotAuth, gotModel, gotContent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		var parsed struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Errorf("request body was not valid JSON: %v (%s)", err, body)
		}
		gotModel = parsed.Model
		if len(parsed.Messages) > 0 {
			gotContent = parsed.Messages[0].Content
		}
		w.Write([]byte(`{"usage":{"total_tokens":9}}`))
	}))
	defer srv.Close()

	res := Send(context.Background(), srv.Client(), Request{
		Provider: testProvider(srv.URL),
		Model:    "vendor/model:free",
		Ref:      "vendor/model:free",
		Prompt:   DefaultPrompt,
		Key:      "test-key-value",
	})

	if !res.OK {
		t.Fatalf("expected success, got status %d: %s", res.Status, res.Detail)
	}
	if res.Status != http.StatusOK {
		t.Errorf("status = %d, want 200", res.Status)
	}
	if res.Tokens != 9 {
		t.Errorf("tokens = %d, want 9", res.Tokens)
	}
	if gotAuth != "Bearer test-key-value" {
		t.Errorf("auth header = %q", gotAuth)
	}
	if gotModel != "vendor/model:free" {
		t.Errorf("model in body = %q", gotModel)
	}
	if gotContent != DefaultPrompt {
		t.Errorf("prompt in body = %q", gotContent)
	}
	if res.Latency <= 0 {
		t.Error("latency was not measured")
	}
}

func TestSendFailureExtractsAndRedacts(t *testing.T) {
	// A provider that echoes a partially masked key in its error body must not
	// leak it into the terminal.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Incorrect key sk-abcdef1234567890 provided"}}`))
	}))
	defer srv.Close()

	res := Send(context.Background(), srv.Client(), Request{
		Provider: testProvider(srv.URL),
		Model:    "m",
		Ref:      "m",
		Key:      "sk-abcdef1234567890",
	})

	if res.OK {
		t.Fatal("expected failure")
	}
	if res.Status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.Status)
	}
	if strings.Contains(res.Detail, "sk-abcdef1234567890") {
		t.Errorf("detail leaked the key: %s", res.Detail)
	}
	if !strings.Contains(res.Detail, "[redacted]") {
		t.Errorf("detail should be redacted, got: %s", res.Detail)
	}
}

func TestSendEscapesPromptIntoBody(t *testing.T) {
	// A prompt containing quotes and a brace must not break the JSON body.
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	res := Send(context.Background(), srv.Client(), Request{
		Provider: testProvider(srv.URL),
		Model:    "m",
		Ref:      "m",
		Prompt:   `he said "hi"`,
		Key:      "k",
	})

	if !res.OK {
		t.Fatalf("probe failed: %s", res.Detail)
	}
	if !json.Valid(body) {
		t.Fatalf("body was not valid JSON: %s", body)
	}
	if !strings.Contains(string(body), `he said \"hi\"`) {
		t.Errorf("prompt was not escaped into the body: %s", body)
	}
}

func TestSendTemplateOutputIsNotRetemplated(t *testing.T) {
	// A model id or prompt containing template syntax must be treated as data.
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	res := Send(context.Background(), srv.Client(), Request{
		Provider: testProvider(srv.URL),
		Model:    "m",
		Ref:      "m",
		Prompt:   "{{.Model}}",
		Key:      "k",
	})
	if !res.OK {
		t.Fatalf("probe failed: %s", res.Detail)
	}
	if !strings.Contains(string(body), `{{.Model}}`) {
		t.Errorf("template was expanded from user data: %s", body)
	}
}

func TestSendWithoutCompletionsIsReported(t *testing.T) {
	p := config.Provider{Name: "bare", BaseURL: "http://example.invalid"}
	res := Send(context.Background(), http.DefaultClient, Request{Provider: p, Model: "m"})
	if res.OK {
		t.Fatal("expected failure for a provider with no completions endpoint")
	}
	if res.Detail == "" {
		t.Error("expected an explanation")
	}
}

func TestPrepareDoesNotAttachCredential(t *testing.T) {
	// Preparing must be safe to display: the key is never part of the result.
	req := Request{
		Provider: testProvider("https://alpha.example"),
		Model:    "m",
		Ref:      "m",
		Key:      "sk-live-secret-value",
	}
	prep, err := Prepare(req)
	if err != nil {
		t.Fatal(err)
	}
	blob := prep.URL + " " + prep.Body + " " + fmt.Sprint(prep.Header)
	if strings.Contains(blob, "sk-live-secret-value") {
		t.Errorf("prepared request leaked the key: %s", blob)
	}

	safe := prep.Safe("Authorization", "Bearer ")
	if got := safe.Header.Get("Authorization"); got != "Bearer [redacted]" {
		t.Errorf("auth header = %q, want a redacted marker", got)
	}
}

func TestSendRecordsRequestAndResponse(t *testing.T) {
	var sentBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sentBody, _ = io.ReadAll(r.Body)
		w.Header().Set("X-Request-Id", "req-42")
		w.Write([]byte(`{"usage":{"total_tokens":11},"ok":true}`))
	}))
	defer srv.Close()

	res := Send(context.Background(), srv.Client(), Request{
		Provider: testProvider(srv.URL),
		Model:    "vendor/m",
		Ref:      "vendor/m",
		Prompt:   DefaultPrompt,
		Key:      "sk-secret",
	})

	if res.Request.URL != srv.URL+"/v1/chat/completions" {
		t.Errorf("recorded url = %q", res.Request.URL)
	}
	if res.Request.Body != string(sentBody) {
		t.Errorf("recorded body does not match what was sent:\n%q\n%q", res.Request.Body, sentBody)
	}
	if res.Response == nil {
		t.Fatal("response was not recorded")
	}
	if res.Response.Header.Get("X-Request-Id") != "req-42" {
		t.Error("response headers were not recorded")
	}
	if !strings.Contains(res.Response.Body, "total_tokens") {
		t.Errorf("response body = %q", res.Response.Body)
	}
	// The recorded request must not carry the real credential.
	if strings.Contains(res.Request.Header.Get("Authorization"), "sk-secret") {
		t.Error("recorded request header leaked the key")
	}
}

func TestResponseBodyIsTruncated(t *testing.T) {
	huge := strings.Repeat("a", maxCapturedBody+500)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(huge))
	}))
	defer srv.Close()

	res := Send(context.Background(), srv.Client(), Request{
		Provider: testProvider(srv.URL),
		Model:    "m",
		Ref:      "m",
		Key:      "sk-secret",
	})
	if res.Response == nil || !res.Response.Truncated {
		t.Fatal("an oversized response should be flagged as truncated")
	}
	if len(res.Response.Body) > maxCapturedBody+16 {
		t.Errorf("captured %d bytes, want the cap enforced", len(res.Response.Body))
	}
}

func TestPrepareRejectsProviderWithoutCompletions(t *testing.T) {
	_, err := Prepare(Request{Provider: config.Provider{Name: "bare"}})
	if err == nil {
		t.Fatal("expected an error")
	}
}
