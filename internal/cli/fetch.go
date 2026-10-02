package cli

import (
	"context"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/bluespada/getfm/pkg/config"
	"github.com/bluespada/getfm/pkg/provider"
)

// catalogRun pairs the catalog with the per-provider fetch outcomes.
type catalogRun struct {
	file    *config.File
	results []provider.FetchResult
}

// failures reports which providers could not be listed.
func (r *catalogRun) failures() []provider.FetchResult {
	var out []provider.FetchResult
	for _, res := range r.results {
		if res.Err != nil {
			out = append(out, res)
		}
	}
	return out
}

// fetchAll loads every provider's catalog concurrently. Providers are fetched
// in parallel because the slowest one should not gate the rest.
func fetchAll(ctx context.Context, client *http.Client, g *globals) (*catalogRun, error) {
	file, err := g.load()
	if err != nil {
		return nil, err
	}
	results := provider.Fetch(ctx, client, file, provider.Options{
		Concurrency: len(file.Providers),
		MaxBytes:    16 << 20,
		Key:         func(p config.Provider) string { return keyFor(p, g) },
	})
	return &catalogRun{file: file, results: results}, nil
}

// newClient builds the shared HTTP client. Redirects are capped so a provider
// cannot bounce the tool somewhere unexpected.
func newClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

// commandContext returns a context that is cancelled when the user interrupts.
func commandContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	notifySignals(ch)
	go func() {
		<-ch
		cancel()
	}()
	return ctx, cancel
}

// sortModels orders models by id so output is stable between runs.
func sortModels(models []provider.Model) {
	slices.SortFunc(models, func(a, b provider.Model) int {
		return strings.Compare(a.ID, b.ID)
	})
}

// keyFor resolves the API key for a provider, preferring a -key flag over the
// declared environment variables.
func keyFor(p config.Provider, g *globals) string {
	return p.Key(g.keyFlags, envLookup)
}

// envLookup is indirected so key resolution stays testable.
var envLookup = os.Getenv
