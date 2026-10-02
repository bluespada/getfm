// Package free decides which models count as free.
//
// Providers advertise free access in several different ways, so the rules are
// configurable and combined: an id suffix such as OpenRouter's ":free", a
// published price of zero, an explicit allow or deny list, or a provider that
// is free outright because it runs on the user's own machine.
package free

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/bluespada/getfm/pkg/provider"
)

// DefaultSuffix is the id marker OpenRouter appends to its free variants.
const DefaultSuffix = ":free"

// Reasons reported for a model that matched. Kept short so they fit a table.
// A model that did not match carries no reason at all, since it is dropped
// before any output is produced.
const (
	ReasonAllow    = "allowed"
	ReasonProvider = "provider"
	ReasonDeclared = "declared"
	ReasonSuffix   = "suffix"
	ReasonZero     = "zero-price"
)

// Rules is a parsed set of free-model criteria.
type Rules struct {
	// Suffix matches model ids ending in this marker. Empty disables it.
	Suffix string
	// Zero matches models whose prompt and completion prices are both 0.
	Zero bool
	// Allow lists ids that are free regardless of any other rule.
	Allow patterns
	// Deny lists ids that are never free. It wins over everything else.
	Deny patterns
}

// patterns is a compiled match list. Entries may use the * and ? wildcards, and
// matching ignores case because providers are inconsistent about how they
// capitalize model ids.
type patterns struct {
	exact map[string]bool
	globs []*regexp.Regexp
}

// compileList parses a comma separated list, rejecting a malformed wildcard
// early rather than silently never matching it.
func compileList(s string) (patterns, error) {
	var p patterns
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.ContainsAny(part, "*?") {
			if p.exact == nil {
				p.exact = map[string]bool{}
			}
			p.exact[strings.ToLower(part)] = true
			continue
		}
		re, err := globRegexp(part)
		if err != nil {
			return p, errf("bad pattern %q: %v", part, err)
		}
		p.globs = append(p.globs, re)
	}
	return p, nil
}

// globRegexp turns a shell-style pattern into a case-insensitive regexp. Unlike
// path.Match, "*" also crosses slashes, so "*free" matches
// "qwen/qwen3.8-27b:free" the way a user would expect.
func globRegexp(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("(?i)^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

func (p patterns) match(s string) bool {
	if p.empty() {
		return false
	}
	s = strings.ToLower(s)
	if p.exact[s] {
		return true
	}
	for _, re := range p.globs {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

func (p patterns) empty() bool { return len(p.exact) == 0 && len(p.globs) == 0 }

// ParseSpec turns a comma separated rule list such as "suffix,zero" into
// rules. Only the rules a spec names are switched on, so "zero" means the zero
// price rule alone and "all" is what asks for both. An empty spec, like
// "none", leaves every implicit rule off, which is useful for showing
// everything a provider offers. Explicit allow and deny entries always apply.
func ParseSpec(spec string, suffix string, allow, deny string) (Rules, error) {
	allowList, err := compileList(allow)
	if err != nil {
		return Rules{}, err
	}
	denyList, err := compileList(deny)
	if err != nil {
		return Rules{}, err
	}

	r := Rules{Allow: allowList, Deny: denyList}
	if spec == "" {
		return r, nil
	}

	for _, part := range strings.Split(spec, ",") {
		switch name := strings.ToLower(strings.TrimSpace(part)); name {
		case "":
		case "suffix", "all":
			if suffix == "" {
				return r, errf("the suffix rule needs a non-empty -suffix value")
			}
			r.Suffix = suffix
			if name == "all" {
				r.Zero = true
			}
		case "zero", "price", "zero-price":
			r.Zero = true
		case "none", "off":
			// Neither implicit rule is switched on below, so this needs no work.
		default:
			return r, errf("unknown free rule %q: want suffix, zero or all", part)
		}
	}
	return r, nil
}

type ruleErr string

func (e ruleErr) Error() string { return string(e) }

func errf(format string, args ...any) error {
	return ruleErr(fmt.Sprintf(format, args...))
}

// Facts are what the catalog says about a provider, as opposed to what the
// rules or the command line say.
type Facts struct {
	// FreeAlways marks a provider where every model is free, such as a local
	// endpoint that costs nothing to run.
	FreeAlways bool
	// FreeIDs are matchers for models the catalog declares free.
	FreeIDs []func(string) bool
}

// Match reports whether a model is free and, if so, which rule said so. The
// reason is empty when the model did not match.
//
// Deny is checked first so an explicit exclusion always wins, then an explicit
// allow, then the provider's own free_always flag, then ids the catalog
// declares free, and finally the suffix and zero-price rules.
func (r Rules) Match(m provider.Model, f Facts) (bool, string) {
	if r.Deny.match(m.ID) {
		return false, ""
	}
	if r.Allow.match(m.ID) {
		return true, ReasonAllow
	}
	if f.FreeAlways {
		return true, ReasonProvider
	}
	if len(f.FreeIDs) > 0 && matchesAny(f.FreeIDs, m.ID) {
		return true, ReasonDeclared
	}
	if r.Suffix != "" && strings.HasSuffix(m.ID, r.Suffix) {
		return true, ReasonSuffix
	}
	// A model with no published pricing cannot be shown to cost zero, so it
	// only qualifies through an explicit allow or the provider flag.
	if r.Zero && m.Priced && m.PromptPrice == 0 && m.CompletionPrice == 0 {
		return true, ReasonZero
	}
	return false, ""
}

// Filter returns the models that satisfy the rules, tagging each with the
// reason it qualified.
func (r Rules) Filter(models []provider.Model, f Facts) []provider.Model {
	out := make([]provider.Model, 0, len(models))
	for _, m := range models {
		ok, reason := r.Match(m, f)
		if !ok {
			continue
		}
		m.Reason = reason
		out = append(out, m)
	}
	return out
}

func matchesAny(matchers []func(string) bool, id string) bool {
	for _, m := range matchers {
		if m(id) {
			return true
		}
	}
	return false
}
